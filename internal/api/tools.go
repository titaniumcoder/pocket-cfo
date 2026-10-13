package api

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
)

type Tool struct {
	Name        string
	Description string
	ReadOnly    bool
	InputSchema *jsonschema.Schema
	Call        func(ctx context.Context, args json.RawMessage) (any, error)
}

func tool[A any](name, description string, readOnly bool, call func(ctx context.Context, args A) (any, error)) Tool {
	schema, err := jsonschema.For[A](nil)
	if err != nil {
		panic(err)
	}
	return Tool{
		Name:        name,
		Description: description,
		ReadOnly:    readOnly,
		InputSchema: schema,
		Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args A
			if err := decodeArgs(raw, &args); err != nil {
				return nil, err
			}
			return call(ctx, args)
		},
	}
}

func decodeArgs(raw json.RawMessage, into any) error {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return errorf(CodeInvalidRequest, "arguments: %v", err)
	}
	return nil
}

func reply[T any](v T, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return v, nil
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

type invoicesArgs struct {
	Year string `json:"year,omitempty" jsonschema:"the year to report, YYYY; omit for every year at once"`
}

type invoiceNumberArgs struct {
	Number string `json:"number" jsonschema:"the invoice number exactly as list_invoices spells it, e.g. INV-0000000002"`
}

type idsResult struct {
	IDs []string `json:"ids"`
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type categoriesResult struct {
	Categories []Category `json:"categories"`
}

type accountsResult struct {
	Accounts []Account `json:"accounts"`
}

type invoiceListResult struct {
	Years    []int     `json:"years"`
	Invoices []Invoice `json:"invoices"`
}

func (s *Service) Tools() []Tool {
	return []Tool{
		tool("list_budget_categories",
			"Every budget category id, with its group, name and kind, under a categories key. A category with a date is a one-off counted only in that month; one with from/until is a recurring cost bounded to that window (both optional); one with amount_changes steps its recurring price at the listed months. These ids are the ONLY legal value for a transaction's category field — never invent one, and never parse budget.json yourself. If the category you need is not listed it does not exist yet: it has to be created in the data repo.",
			true, func(ctx context.Context, _ emptyArgs) (any, error) {
				out, err := s.Categories(ctx)
				return reply(categoriesResult{Categories: out}, err)
			}),

		tool("get_budget",
			"The plan for a period, with overrides already applied. period is YYYY-MM for one month or YYYY for twelve month buckets. A category with a date is a one-off counted only in that month; one with from/until is a recurring cost counted inside that window (either bound optional), so it contributes nothing before from or after until. A category with amount_changes pays the latest scheduled change at or before the month — so a month bucket already reflects a price that steps in, and each category's whole amount_changes list is reported for you to read.. Read those figures rather than recomputing the taxes yourself. For a month, sha is budget.json's current sha.",
			true, func(ctx context.Context, a periodArgs) (any, error) {
				if len(a.Period) == 4 {
					return reply(s.BudgetForYear(ctx, a.Period))
				}
				return reply(s.BudgetForMonth(ctx, a.Period))
			}),

		tool("get_actuals",
			"The committed document for one month, read straight from the data repo. This file is the source of truth: where it disagrees with your own recollection, it wins. Returns not_found when the month has never been reconciled. Data changes are not made through this API — the file is edited in the data repo and committed by hand; whatever is committed is what this reports.",
			true, func(ctx context.Context, a monthArgs) (any, error) {
				return reply(s.ActualsFor(ctx, a.Month))
			}),

		tool("search_transactions",
			"Search committed history by statement description, returning the category each line was assigned to, with the month and id beside each hit. Use this before asking the user about a line you cannot place: a past match is a strong answer, no match means ask the user. only_untracked lists everything still waiting on a decision.",
			true, func(ctx context.Context, a searchArgs) (any, error) {
				return reply(s.Search(ctx, SearchQuery{
					Query: a.Query, From: a.From, To: a.To, Category: a.Category, Account: a.Account,
					IncludeIgnored: a.IncludeIgnored, OnlyUntracked: a.OnlyUntracked,
					Limit: a.Limit, Years: a.Years,
				}))
			}),

		tool("list_accounts",
			"The account names the rest of the system uses, under an accounts key, each with the kind of money it holds — company or private — and as_of, the date it was last read off the bank. "+
				"Spell a transaction's account field exactly as listed here. "+
				"as_of is the newest of however many readings that account has; it says how current the balance figures are, and it is not the set of accounts a statement line may arrive on. "+
				"An as_of older than the month that just closed means that account is due a fresh reading.",
			true, func(ctx context.Context, _ emptyArgs) (any, error) {
				out, err := s.AccountsList(ctx)
				return reply(accountsResult{Accounts: out}, err)
			}),

		tool("list_invoices",
			"Every invoice the dashboard shows, under an invoices key — issued and draft alike, each with its number, title, recipient, issue and due date, grand total in cents, a state: draft, issued, overdue (due date passed unpaid), or paid with the date it was paid on, and the pdfs its state can serve — draft, original, paid — which GET /api/invoices/{number}/pdf?variant=… downloads (REST only, never through MCP). years lists the years invoices exist for; pass one as year to see only that year. The same numbers the invoicing page renders, and the only source of invoice numbers — spell them exactly as listed here. Drafts cannot be paid; an invoice you do not see does not exist yet.",
			true, func(ctx context.Context, a invoicesArgs) (any, error) {
				return reply(s.Invoices(ctx, a.Year))
			}),
	}
}
