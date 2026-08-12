package api

import (
	"context"
	"encoding/json"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
	OnlyUntracked  bool   `json:"only_untracked,omitempty" jsonschema:"only lines still waiting on a decision, i.e. carrying an untracked note. This is how you find the cash you parked earlier; untracked lines are in the results either way"`
	Limit          int    `json:"limit,omitempty" jsonschema:"maximum results, default 50, capped at 500"`
	Years          []int  `json:"years,omitempty" jsonschema:"which years to scan; derived from from/to when those are given, else the current year"`
}

type reconciliationArgs struct {
	Year int `json:"year,omitempty" jsonschema:"the year to report, defaults to the current one"`
}

type splitArg struct {
	Amount    float64 `json:"amount" jsonschema:"euros for this part, same sign convention as the line. Never 0, and the parts must add up to the line's own amount"`
	Category  string  `json:"category,omitempty" jsonschema:"the budget category id this part belongs to"`
	Ignored   string  `json:"ignored,omitempty" jsonschema:"why this part is not a budget expense, e.g. 'moved to savings'"`
	Untracked string  `json:"untracked,omitempty" jsonschema:"why this part is not decided yet, e.g. 'the rest is still in my wallet'. Exactly one of category, ignored or untracked per part"`
}

type txArg struct {
	ID          string     `json:"id" jsonschema:"stable id for this statement line, derived deterministically from account+date+amount+description with a -2 suffix on collision. Re-sending the same statement must produce the same ids, which is what makes a repeat import a no-op instead of a duplicate"`
	Date        string     `json:"date" jsonschema:"YYYY-MM-DD. This decides which month the line is filed under, so it must be the date the bank gave it"`
	Description string     `json:"description" jsonschema:"the statement line as the bank wrote it, not a tidied-up version"`
	Amount      float64    `json:"amount" jsonschema:"euros. POSITIVE = money out; a refund is negative against the same category. Never 0"`
	Account     string     `json:"account" jsonschema:"which account it was charged to, spelled exactly as list_accounts spells it"`
	Category    string     `json:"category,omitempty" jsonschema:"the budget category id from list_budget_categories. Never invent one"`
	Ignored     string     `json:"ignored,omitempty" jsonschema:"why this line is not a budget expense, e.g. 'salary', 'transfer to savings'. A reason, never a bare yes. This is a decision that it does not belong in the figures"`
	Untracked   string     `json:"untracked,omitempty" jsonschema:"why this line is not decided YET, e.g. 'ATM withdrawal, cash not spent yet'. Use this instead of guessing a category or stopping to ask; it shows on the spending page and marks the month until it is resolved. Different from ignored, which means it is deliberately not an expense"`
	Splits      []splitArg `json:"splits,omitempty" jsonschema:"set instead of category/ignored/untracked when one line paid for more than one thing. Two or more parts, adding up to the line's amount"`
}

type coverageArg struct {
	Account    string `json:"account" jsonschema:"the account this range was read from, spelled as the transactions spell it"`
	From       string `json:"from" jsonschema:"first day read, inclusive, YYYY-MM-DD. Must be in the same month as to"`
	To         string `json:"to" jsonschema:"last day read, inclusive, YYYY-MM-DD. This is what stops a mid-month import reading as a spectacular underspend, and it can only ever move forward"`
	ImportedAt string `json:"imported_at" jsonschema:"when you read the statement, YYYY-MM-DD. Informational"`
}

type editArg struct {
	ID        string     `json:"id" jsonschema:"the id of the transaction to change, as get_actuals or search_transactions reported it"`
	Month     string     `json:"month,omitempty" jsonschema:"the month it is filed under, YYYY-MM, exactly as get_actuals or search_transactions returned it beside the id. Optional, but always worth sending: without it the id is looked up in the deployed copy, which lags by one deploy"`
	Category  string     `json:"category,omitempty" jsonschema:"reattribute the line to this budget category id, clearing whatever it had before"`
	Ignored   string     `json:"ignored,omitempty" jsonschema:"mark the line as not a budget expense, with the reason, clearing whatever it had before"`
	Untracked string     `json:"untracked,omitempty" jsonschema:"park the line as not decided yet, with a note, clearing whatever it had before"`
	Splits    []splitArg `json:"splits,omitempty" jsonschema:"replace the line's attribution with these parts, which must add up to its amount. Set exactly one of category, ignored, untracked or splits per edit"`
}

type addArgs struct {
	Transactions []txArg       `json:"transactions,omitempty" jsonschema:"the statement lines to record. Send only lines not already in the file. May be omitted when you are only reporting coverage"`
	Coverage     []coverageArg `json:"coverage,omitempty" jsonschema:"which account was read through which dates. One range per month — a range may not cross a month boundary. Required for any month that has never been reconciled"`
}

type editArgs struct {
	Edits  []editArg `json:"edits" jsonschema:"every change you want made, as one batch. A line you do not name here is not touched"`
	Reason string    `json:"reason,omitempty" jsonschema:"why, in one line; lands in the commit message"`
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func splitsOf(args []splitArg) []actualsdata.Split {
	if len(args) == 0 {
		return nil
	}
	out := make([]actualsdata.Split, 0, len(args))
	for _, s := range args {
		out = append(out, actualsdata.Split{
			Amount:    s.Amount,
			Category:  optional(s.Category),
			Ignored:   optional(s.Ignored),
			Untracked: optional(s.Untracked),
		})
	}
	return out
}

func (a addArgs) request() AddRequest {
	req := AddRequest{}
	for _, t := range a.Transactions {
		req.Transactions = append(req.Transactions, actualsdata.Transaction{
			Id: t.ID, Date: t.Date, Description: t.Description,
			Amount: t.Amount, Account: t.Account,
			Category:  optional(t.Category),
			Ignored:   optional(t.Ignored),
			Untracked: optional(t.Untracked),
			Splits:    splitsOf(t.Splits),
		})
	}
	for _, c := range a.Coverage {
		req.Coverage = append(req.Coverage, actualsdata.Coverage{
			Account: c.Account, From: c.From, To: c.To, ImportedAt: c.ImportedAt,
		})
	}
	return req
}

func (a editArgs) request() EditRequest {
	req := EditRequest{Reason: a.Reason}
	for _, e := range a.Edits {
		req.Edits = append(req.Edits, Edit{
			ID: e.ID, Month: e.Month,
			Category:  optional(e.Category),
			Ignored:   optional(e.Ignored),
			Untracked: optional(e.Untracked),
			Splits:    splitsOf(e.Splits),
		})
	}
	return req
}

type moveArgs struct {
	CategoryID string `json:"category_id" jsonschema:"the budget category to move"`
	FromMonth  string `json:"from_month" jsonschema:"the month it is currently planned for, YYYY-MM"`
	ToMonth    string `json:"to_month" jsonschema:"the month it was actually charged in, YYYY-MM"`
	Reason     string `json:"reason" jsonschema:"why it moved; lands in the commit message"`
	BaseSHA    string `json:"base_sha" jsonschema:"the sha of budget.json this edit is based on"`
}

type categoriesResult struct {
	Categories []Category `json:"categories"`
}

type accountsResult struct {
	Accounts []Account `json:"accounts"`
}

type reconciliationResult struct {
	Months []MonthStatus `json:"months"`
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
		"Every budget category id, with its group, name and kind, under a categories key. These ids are the ONLY legal value for a transaction's category field — never invent one, and never parse budget.json yourself. If the category you need is not listed it does not exist yet: ask for it to be created rather than guessing an id.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		out, err := s.Categories(ctx)
		return result(categoriesResult{Categories: out}, err)
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
		"The committed document for one month. This file is the source of truth: where it disagrees with your own recollection, it wins — read it before adding lines, so you send only what is missing. Returns not_found when the month has never been reconciled. Writes take no sha, so nothing here has to be passed back; the id and month beside each line are what add_transactions and edit_transactions work from.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, a monthArgs) (*mcp.CallToolResult, any, error) {
		return result(s.ActualsFor(ctx, a.Month))
	})

	mcp.AddTool(server, tool("search_transactions",
		"Search committed history by statement description, returning the category each line was assigned to, with the month and id needed to edit it. Use this before asking the user about a line you cannot place: a past match is a strong answer, no match means ask — or record it as untracked and come back to it. only_untracked lists everything still waiting on a decision.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, a searchArgs) (*mcp.CallToolResult, any, error) {
		return result(s.Search(ctx, SearchQuery{
			Query: a.Query, From: a.From, To: a.To, Category: a.Category, Account: a.Account,
			IncludeIgnored: a.IncludeIgnored, OnlyUntracked: a.OnlyUntracked, Limit: a.Limit, Years: a.Years,
		}))
	})

	mcp.AddTool(server, tool("get_reconciliation_status",
		"Per month for a year, under a months key: coverage, transaction counts, planned versus actual, how much is still untracked, and any one-off charged in a month other than the one it is budgeted for. Tells you where you left off — a month with untracked money is not finished, and untracked_cents is never part of actual_cents.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, a reconciliationArgs) (*mcp.CallToolResult, any, error) {
		year := a.Year
		if year == 0 {
			year = s.now().Year()
		}
		out, err := s.Reconciliation(ctx, year)
		return result(reconciliationResult{Months: out}, err)
	})

	mcp.AddTool(server, tool("list_accounts",
		"The account names the rest of the system uses, under an accounts key. Spell a transaction's account field exactly as listed here.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		out, err := s.AccountsList(ctx)
		return result(accountsResult{Accounts: out}, err)
	})

	mcp.AddTool(server, tool("add_transactions",
		"Record statement lines that are not in the file yet. Send ONLY new lines: this never touches, reorders or removes anything already recorded, so there is no whole-month document and no base_sha. "+
			"Do NOT send a month — each line's date decides which month it is filed under, so a statement crossing a month boundary is one call that writes two months. "+
			"A line whose id is already recorded and identical is skipped and reported back, so re-sending the same statement is safe and commits nothing; a line whose id is already recorded but differs is refused — use edit_transactions to change a recorded line. "+
			"Every line takes exactly one of category, ignored, untracked or splits; a line that paid for more than one thing carries splits instead, whose parts must add up to the line's amount and each take exactly one of the three. "+
			"Use untracked, with a note, for money you cannot place — that is what it is for, and it is better than guessing a category or leaving the reconciliation unfinished. "+
			"Send coverage for any month that has never been reconciled; without it that month cannot be created. Each month written is a git commit that redeploys the app.",
		false), func(ctx context.Context, _ *mcp.CallToolRequest, a addArgs) (*mcp.CallToolResult, any, error) {
		return result(s.AddTransactions(ctx, a.request()))
	})

	mcp.AddTool(server, tool("edit_transactions",
		"Change what already-recorded transactions are attributed to — the common correction, e.g. every Parkmart line in August to Groceries. Send all the changes as one edits array: one call, one commit per month touched. "+
			"Each edit names a transaction id and exactly one of category, ignored, untracked or splits, which REPLACES whatever that line carried before — this is how an ignored line becomes a category, or a category becomes untracked. "+
			"It CANNOT change a date, amount, description or account, cannot add a line, and cannot remove one; anything you leave out of the array is left alone. There is no removal in this API at all. "+
			"Pass month with each edit — get_actuals and search_transactions both return it beside the id. It is optional, but the fallback lookup reads the deployed copy, which lags by one deploy and so cannot see a line added minutes ago. "+
			"There is no base_sha: only the ids you name are touched, so someone else's change to another line merges rather than being lost. "+
			"If any id does not exist, nothing in that month is written and the missing ids come back. reason lands in the commit message.",
		false), func(ctx context.Context, _ *mcp.CallToolRequest, a editArgs) (*mcp.CallToolResult, any, error) {
		return result(s.EditTransactions(ctx, a.request()))
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
