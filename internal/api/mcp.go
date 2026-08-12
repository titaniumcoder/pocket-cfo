package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This file is the only place the MCP SDK is imported, and no SDK type crosses
// into the service: deleting it leaves the REST surface and every service
// method untouched. The conformance tests drive /mcp over raw HTTP with
// hand-written JSON-RPC rather than the SDK's client, so they would equally
// validate a hand-rolled replacement.
//
// The dependency is a departure from AGENTS.md's stdlib-first rule, recorded
// there: this is a moving external wire protocol with strict framing where
// being subtly wrong presents as silence — Hermes seeing no tools at the
// moment you need the reconciliation — rather than as an error.

// MCPHandler exposes the service as MCP tools over streamable HTTP.
//
// Authentication is NOT here: the caller wraps this in the same bearer gate as
// /api/, so initialize and tools/list are refused before a byte of JSON-RPC is
// parsed and an unauthenticated caller learns nothing about the tool surface.
func (s *Service) MCPHandler(version string) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "pocket-cfo", Version: version}, nil)
	s.registerTools(server)
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
}

type emptyArgs struct{}

type monthArgs struct {
	Month string `json:"month" jsonschema:"the month as YYYY-MM, e.g. 2026-08"`
}

type periodArgs struct {
	Period string `json:"period" jsonschema:"YYYY-MM for one month, or YYYY for twelve month buckets"`
}

type searchArgs struct {
	Query          string `json:"query,omitempty" jsonschema:"case-insensitive substring of the statement description; empty matches everything"`
	From           string `json:"from,omitempty" jsonschema:"earliest month to search, YYYY-MM"`
	To             string `json:"to,omitempty" jsonschema:"latest month to search, YYYY-MM"`
	Category       string `json:"category,omitempty" jsonschema:"only transactions assigned to this budget category id"`
	Account        string `json:"account,omitempty" jsonschema:"only transactions on this account"`
	IncludeIgnored bool   `json:"include_ignored,omitempty" jsonschema:"include lines recorded as not-an-expense"`
	Limit          int    `json:"limit,omitempty" jsonschema:"maximum results, default 50, capped at 500"`
	Years          []int  `json:"years,omitempty" jsonschema:"which years to scan; derived from from/to when those are given, else the current year"`
}

type reconciliationArgs struct {
	Year int `json:"year,omitempty" jsonschema:"the year to report, defaults to the current one"`
}

type putActualsArgs struct {
	Month         string          `json:"month" jsonschema:"the month being replaced, YYYY-MM"`
	Document      json.RawMessage `json:"document" jsonschema:"the complete month document"`
	BaseSHA       string          `json:"base_sha" jsonschema:"the sha from get_actuals; empty when the month has never been committed"`
	AllowRemovals string          `json:"allow_removals,omitempty" jsonschema:"why data may disappear; required only when something would"`
}

type moveArgs struct {
	CategoryID string `json:"category_id" jsonschema:"the budget category to move"`
	FromMonth  string `json:"from_month" jsonschema:"the month it is currently planned for, YYYY-MM"`
	ToMonth    string `json:"to_month" jsonschema:"the month it was actually charged in, YYYY-MM"`
	Reason     string `json:"reason" jsonschema:"why it moved; lands in the commit message"`
	BaseSHA    string `json:"base_sha" jsonschema:"the sha of budget.json this edit is based on"`
}

func (s *Service) registerTools(server *mcp.Server) {
	tool := func(name, desc string, readOnly bool) *mcp.Tool {
		return &mcp.Tool{
			Name:        name,
			Description: desc,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
		}
	}

	mcp.AddTool(server, tool("list_budget_categories",
		"Every budget category id, with its group, name and kind. These ids are the ONLY legal value for a transaction's category field — never invent one, and never parse budget.json yourself.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		return result(s.Categories(ctx))
	})

	mcp.AddTool(server, tool("get_budget",
		"The plan for a period, with overrides already applied. period is YYYY-MM for one month or YYYY for twelve month buckets. A category with a date is a one-off counted only in that month.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, a periodArgs) (*mcp.CallToolResult, any, error) {
		if len(a.Period) == 4 {
			return result(s.BudgetForYear(ctx, a.Period))
		}
		return result(s.BudgetForMonth(ctx, a.Period))
	})

	mcp.AddTool(server, tool("get_actuals",
		"The committed document for one month, with the sha to pass back as put_actuals' base_sha. This file is the source of truth: where it disagrees with your own recollection, it wins. Returns not_found when the month has never been reconciled — that is the case where base_sha is empty.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, a monthArgs) (*mcp.CallToolResult, any, error) {
		return result(s.ActualsFor(ctx, a.Month))
	})

	mcp.AddTool(server, tool("search_transactions",
		"Search committed history by statement description, returning the category each line was assigned to. Use this before asking the user about a line you cannot place: a past match is a strong answer, no match means ask.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, a searchArgs) (*mcp.CallToolResult, any, error) {
		return result(s.Search(ctx, SearchQuery{
			Query: a.Query, From: a.From, To: a.To, Category: a.Category, Account: a.Account,
			IncludeIgnored: a.IncludeIgnored, Limit: a.Limit, Years: a.Years,
		}))
	})

	mcp.AddTool(server, tool("get_reconciliation_status",
		"Per month for a year: coverage, transaction counts, planned versus actual, and any one-off charged in a month other than the one it is budgeted for. Tells you where you left off.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, a reconciliationArgs) (*mcp.CallToolResult, any, error) {
		year := a.Year
		if year == 0 {
			year = s.now().Year()
		}
		return result(s.Reconciliation(ctx, year))
	})

	mcp.AddTool(server, tool("list_accounts",
		"The account names the rest of the system uses. Spell a transaction's account field exactly as listed here.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		return result(s.AccountsList(ctx))
	})

	mcp.AddTool(server, tool("put_actuals",
		"Commit one month. This is a WHOLE-MONTH REPLACEMENT: any transaction you omit is a removal and will be refused unless allow_removals gives a reason. Submit every transaction already recorded, not just new ones. base_sha comes from get_actuals; a conflict means the file changed underneath you, so re-read, merge and resubmit — never retry blind. Each accepted call is a git commit that redeploys the app.",
		false), func(ctx context.Context, _ *mcp.CallToolRequest, a putActualsArgs) (*mcp.CallToolResult, any, error) {
		return result(s.PutActuals(ctx, a.Month, PutRequest{
			Document: a.Document, BaseSHA: a.BaseSHA, AllowRemovals: a.AllowRemovals,
		}))
	})

	mcp.AddTool(server, tool("move_planned_expense",
		"Move one one-off cost to the month it was actually charged in, when get_reconciliation_status reports it as mistimed. Changes that category's date and nothing else — it cannot alter an amount or touch anything that recurs monthly. reason is required and lands in the commit message.",
		false), func(ctx context.Context, _ *mcp.CallToolRequest, a moveArgs) (*mcp.CallToolResult, any, error) {
		return result(s.MovePlannedExpense(ctx, MoveRequest{
			CategoryID: a.CategoryID, FromMonth: a.FromMonth, ToMonth: a.ToMonth,
			Reason: a.Reason, BaseSHA: a.BaseSHA,
		}))
	})
}

// result adapts a service return into a tool result. A service error becomes a
// tool error carrying the same code the REST adapter would map, so the two
// surfaces say the same thing.
func result[T any](out T, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: errorText(err)}},
		}, nil, nil
	}
	return nil, out, nil
}

func errorText(err error) string {
	if e, ok := err.(*Error); ok {
		b, mErr := json.Marshal(map[string]any{"code": e.Code, "message": e.Message, "details": e.Details})
		if mErr == nil {
			return string(b)
		}
	}
	return err.Error()
}
