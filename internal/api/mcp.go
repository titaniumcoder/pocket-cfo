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
	OnlyMovements  bool   `json:"only_movements,omitempty" jsonschema:"only lines marked as money crossing between the company and its owner. This is how you find every transfer in a period without reading each month's document"`
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

type setInvoicePaidArgs struct {
	Invoice string `json:"invoice" jsonschema:"the invoice number exactly as list_invoices spells it, e.g. INV-0000000002"`
	Paid    bool   `json:"paid" jsonschema:"true records the payment, false removes the payment record again (a no-op when there is none)"`
	Date    string `json:"date,omitempty" jsonschema:"the day the money arrived, YYYY-MM-DD, as the bank shows it. Required when paid is true, and never in the future — a payment is read off the bank, not projected"`
	Note    string `json:"note,omitempty" jsonschema:"optional free text carried on the payment entry — there is deliberately no account or method field, so put 'received on the company account at Bank X' here if it matters"`
	Reason  string `json:"reason,omitempty" jsonschema:"why the payment is recorded, corrected or removed; lands in the commit message"`
}

type splitArg struct {
	Amount    float64 `json:"amount" jsonschema:"euros for this part, same sign convention as the line. Never 0, and the parts must add up to the line's own amount"`
	Category  string  `json:"category,omitempty" jsonschema:"the budget category id this part belongs to"`
	Ignored   string  `json:"ignored,omitempty" jsonschema:"why this part is not a budget expense, e.g. 'moved to savings'"`
	Untracked string  `json:"untracked,omitempty" jsonschema:"why this part is not decided yet, e.g. 'the rest is still in my wallet'. Exactly one of category, ignored or untracked per part"`
	Movement  string  `json:"movement,omitempty" jsonschema:"what this line moved between the company and its owner, set ALONGSIDE ignored rather than instead of it. One of salary_transfer, owner_draw, dividend_payout, owner_contribution, corporate_tax, dividend_tax. The first four settle the director's loan; the last two leave the company for the state and settle nothing. RECORD IT ONCE, ON THE COMPANY STATEMENT: both sides of a transfer are imported, and marking both counts it twice. The sign enforces it — everything except owner_contribution must be money OUT of the company, so the mirror line on the private statement is refused"`
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
	Movement    string     `json:"movement,omitempty" jsonschema:"what this line moved between the company and its owner, set ALONGSIDE ignored rather than instead of it. One of salary_transfer, owner_draw, dividend_payout, owner_contribution, corporate_tax, dividend_tax. The first four settle the director's loan; the last two leave the company for the state and settle nothing. RECORD IT ONCE, ON THE COMPANY STATEMENT: both sides of a transfer are imported, and marking both counts it twice. The sign enforces it — everything except owner_contribution must be money OUT of the company, so the mirror line on the private statement is refused"`
}

type coverageArg struct {
	Account    string `json:"account" jsonschema:"the account this range was read from, spelled as the transactions spell it"`
	From       string `json:"from" jsonschema:"first day read, inclusive, YYYY-MM-DD. Must be in the same month as to"`
	To         string `json:"to" jsonschema:"last day read, inclusive, YYYY-MM-DD. This is what stops a mid-month import reading as a spectacular underspend, and it can only ever move forward"`
	ImportedAt string `json:"imported_at" jsonschema:"when you read the statement, YYYY-MM-DD. Informational"`
}

type editArg struct {
	ID        string     `json:"id" jsonschema:"the id of the transaction to change, as get_actuals or search_transactions reported it"`
	Month     string     `json:"month,omitempty" jsonschema:"the month it is filed under, YYYY-MM, exactly as get_actuals or search_transactions returned it beside the id. Optional, but always worth sending: without it the id is looked up by scanning the months around now, which is both slower and bounded to a three-year window"`
	Category  string     `json:"category,omitempty" jsonschema:"reattribute the line to this budget category id, clearing whatever it had before"`
	Ignored   string     `json:"ignored,omitempty" jsonschema:"mark the line as not a budget expense, with the reason, clearing whatever it had before"`
	Untracked string     `json:"untracked,omitempty" jsonschema:"park the line as not decided yet, with a note, clearing whatever it had before"`
	Splits    []splitArg `json:"splits,omitempty" jsonschema:"replace the line's attribution with these parts, which must add up to its amount. Set exactly one of category, ignored, untracked or splits per edit"`
	Movement  string     `json:"movement,omitempty" jsonschema:"send WITH ignored to mark what this line moved between the company and its owner. An edit that does not send it clears it, so re-attributing a transfer to a category cannot leave the marker behind. what this line moved between the company and its owner, set ALONGSIDE ignored rather than instead of it. One of salary_transfer, owner_draw, dividend_payout, owner_contribution, corporate_tax, dividend_tax. The first four settle the director's loan; the last two leave the company for the state and settle nothing. RECORD IT ONCE, ON THE COMPANY STATEMENT: both sides of a transfer are imported, and marking both counts it twice. The sign enforces it — everything except owner_contribution must be money OUT of the company, so the mirror line on the private statement is refused"`
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

func optionalMovement(s string) *actualsdata.Movement {
	if s == "" {
		return nil
	}
	m := actualsdata.Movement(s)
	return &m
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
			Movement:  optionalMovement(s.Movement),
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
			Movement:  optionalMovement(t.Movement),
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
			Movement:  optionalMovement(e.Movement),
			Splits:    splitsOf(e.Splits),
		})
	}
	return req
}

type balanceArgs struct {
	Account string  `json:"account" jsonschema:"which account was read, spelled exactly as list_accounts spells it. An account is never created here, and the director's loan is refused — it is not a bank account and nothing reads it off one"`
	AsOf    string  `json:"as_of" jsonschema:"the MONTH-END date the balance was read, YYYY-MM-DD. It must be the LAST day of the month — 2026-07-31, 2026-06-30, 2026-02-28 — because this is the closing balance of that month. A mid-month date is refused"`
	Balance float64 `json:"balance" jsonschema:"euros in the account at the end of that day, as the bank shows it. Decimals allowed, and negative for an overdrawn account"`
	Note    string  `json:"note,omitempty" jsonschema:"optional free text about this one reading, shown beside the account in the month it anchors. Not used in any computation"`
}

type moveArgs struct {
	CategoryID string `json:"category_id" jsonschema:"the budget category to move"`
	FromMonth  string `json:"from_month" jsonschema:"the month it is currently planned for, YYYY-MM"`
	ToMonth    string `json:"to_month" jsonschema:"the month it was actually charged in, YYYY-MM"`
	Reason     string `json:"reason" jsonschema:"why it moved; lands in the commit message"`
	BaseSHA    string `json:"base_sha" jsonschema:"the sha of budget.json this edit is based on, as get_budget reports it in its sha field. A stale one comes back as a conflict carrying the current sha, so re-read and try again"`
}

type amountChangeArgs struct {
	CategoryID    string   `json:"category_id" jsonschema:"the budget category to change, from list_budget_categories"`
	FromMonth     string   `json:"from_month" jsonschema:"the month the new amount takes effect, YYYY-MM. Must be a future month — a month that is already in force cannot be re-planned here"`
	Amount        *float64 `json:"amount,omitempty" jsonschema:"euros, decimals allowed. The category's amount from from_month on, until a later scheduled change supersedes it. Required unless remove is set"`
	MinimalAmount *float64 `json:"minimal_amount,omitempty" jsonschema:"optional reduced amount minimal-budget mode uses for the months this change is in force. Absent, minimal mode falls back to this change's own amount"`
	Remove        bool     `json:"remove,omitempty" jsonschema:"set instead of amount to undo the scheduled change for from_month. Never set both"`
	Reason        string   `json:"reason" jsonschema:"why the price changes (or the change was called off); lands in the commit message"`
	BaseSHA       string   `json:"base_sha" jsonschema:"the sha of budget.json this edit is based on, as get_budget reports it in its sha field. A stale one comes back as a conflict carrying the current sha, so re-read and try again"`
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

type invoiceListResult struct {
	Years    []int     `json:"years"`
	Invoices []Invoice `json:"invoices"`
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
		"Every budget category id, with its group, name and kind, under a categories key. A category with a date is a one-off counted only in that month; one with from/until is a recurring cost bounded to that window (both optional); one with amount_changes steps its recurring price at the listed months, and the whole scheduled list is reported so you can see which months are already spoken for before calling schedule_amount_change. These ids are the ONLY legal value for a transaction's category field — never invent one, and never parse budget.json yourself. If the category you need is not listed it does not exist yet: ask for it to be created rather than guessing an id.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		out, err := s.Categories(ctx)
		return result(categoriesResult{Categories: out}, err)
	})

	mcp.AddTool(server, tool("get_budget",
		"The plan for a period, with overrides already applied. period is YYYY-MM for one month or YYYY for twelve month buckets. A category with a date is a one-off counted only in that month; one with from/until is a recurring cost counted inside that window (either bound optional), so it contributes nothing before from or after until. A category with amount_changes pays the latest scheduled change at or before the month — so a month bucket already reflects a price that steps in, and each category's whole amount_changes list is reported for you to read. dividends lists any distribution planned for the month with both taxes already worked out — the gross, the company profit tax it costs the company, the dividend tax withheld and what actually reaches the owner. Read those figures rather than recomputing them from get_finance_config, which is easy to resolve to the wrong month. A move_planned_expense or schedule_amount_change you made is reflected at once; a budget.json edited directly appears only after that commit has deployed. For a month, sha is budget.json's current sha — that is the base_sha move_planned_expense and schedule_amount_change need.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, a periodArgs) (*mcp.CallToolResult, any, error) {
		if len(a.Period) == 4 {
			return result(s.BudgetForYear(ctx, a.Period))
		}
		return result(s.BudgetForMonth(ctx, a.Period))
	})

	mcp.AddTool(server, tool("get_actuals",
		"The committed document for one month. This file is the source of truth: where it disagrees with your own recollection, it wins — read it before adding lines, so you send only what is missing. Returns not_found when the month has never been reconciled. Writes take no sha, so nothing here has to be passed back; the id and month beside each line are what add_transactions and edit_transactions work from. This reads the data repo directly whenever writes are configured, so a month you wrote through this API AND a change made by hand in the repo both read back immediately — which means this can be ahead of search_transactions and get_reconciliation_status, which lag until the commit has deployed. Where they disagree, this one is current.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, a monthArgs) (*mcp.CallToolResult, any, error) {
		return result(s.ActualsFor(ctx, a.Month))
	})

	mcp.AddTool(server, tool("search_transactions",
		"Search committed history by statement description, returning the category each line was assigned to, with the month and id needed to edit it. Use this before asking the user about a line you cannot place: a past match is a strong answer, no match means ask — or record it as untracked and come back to it. only_untracked lists everything still waiting on a decision. EVENTUALLY CONSISTENT: anything you wrote through this API is searchable at once, but a month edited directly in the data repo, or by another running instance, only appears after that commit has deployed — so a search that finds nothing is not proof that nothing is there.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, a searchArgs) (*mcp.CallToolResult, any, error) {
		return result(s.Search(ctx, SearchQuery{
			Query: a.Query, From: a.From, To: a.To, Category: a.Category, Account: a.Account,
			IncludeIgnored: a.IncludeIgnored, OnlyUntracked: a.OnlyUntracked, OnlyMovements: a.OnlyMovements,
			Limit: a.Limit, Years: a.Years,
		}))
	})

	mcp.AddTool(server, tool("get_reconciliation_status",
		"Per month for a year, under a months key: coverage, transaction counts, planned versus actual, how much is still untracked, and any one-off charged in a month other than the one it is budgeted for. Tells you where you left off — a month with untracked money is not finished, and untracked_cents is never part of actual_cents. Safe to call straight after a write to check it landed: a month you wrote through this API is reflected at once. EVENTUALLY CONSISTENT for everything else — a change made directly in the data repo, or by another running instance, appears only after that commit has deployed.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, a reconciliationArgs) (*mcp.CallToolResult, any, error) {
		year := a.Year
		if year == 0 {
			year = s.now().Year()
		}
		out, err := s.Reconciliation(ctx, year)
		return result(reconciliationResult{Months: out}, err)
	})

	mcp.AddTool(server, tool("list_accounts",
		"The account names the rest of the system uses, under an accounts key, each with the kind of money it holds — company or private — and as_of, the date it was last read off the bank. "+
			"Spell a transaction's account field exactly as listed here. "+
			"as_of is the newest of however many readings that account has; it says how current the balance figures are, and it is not the set of accounts a statement line may arrive on. "+
			"An as_of older than the month that just closed means that account is due a fresh reading — record_account_balance is how you give it one. "+
			"One entry has kind director_loan and is NOT a bank account: it is the running balance between the company and its owner, listed here because it sits with the accounts on the page. There is no statement behind it, no coverage to report for it, and record_account_balance refuses it — read it with get_director_loan and otherwise leave it alone. "+
			"A balance you recorded through this API is reflected here at once; an account added or edited directly in the data repo appears only after that commit has deployed.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		out, err := s.AccountsList(ctx)
		return result(accountsResult{Accounts: out}, err)
	})

	mcp.AddTool(server, tool("list_invoices",
		"Every invoice the dashboard shows, under an invoices key — issued and draft alike, each with its number, title, recipient, issue and due date, grand total in cents, and a state: draft, issued, overdue (due date passed unpaid), or paid with the date it was paid on. years lists the years invoices exist for; pass one as year to see only that year. The same numbers the invoicing page renders, and the ONLY source of invoice numbers — spell set_invoice_paid's invoice field exactly as listed here. Drafts cannot be paid; an invoice you do not see does not exist yet. A payment recorded through set_invoice_paid is reflected here at once, since the paid list is read live; the invoice documents themselves lag until a commit has deployed.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, a invoicesArgs) (*mcp.CallToolResult, any, error) {
		return result(s.Invoices(ctx, a.Year))
	})

	mcp.AddTool(server, tool("set_invoice_paid",
		"Record that an invoice was paid, or take the record back — the one mutable thing about an invoice, kept deliberately OUTSIDE the invoice document (which is write-once once issued) in data/paid-invoices.json. "+
			"\"paid\": true requires date, the day the money arrived, YYYY-MM-DD, as the bank shows it — never a future date, and never 'when the invoice was issued'. "+
			"Recording it again with a different date corrects the record; paid false removes the entry. Both directions are IDEMPOTENT: re-sending an identical payment, or unmarking an invoice that was never marked, changes nothing and commits nothing. "+
			"There is no amount and no account field BY DESIGN (see ARCHITECTURE.md §3.6): payment is one date per invoice, and the optional note is where a bank reference or which account it landed on goes, as free text. "+
			"A draft is refused — a draft is never paid — and so is an invoice that does not exist; list_invoices is the source of numbers. "+
			"reason is optional and lands in the commit message. Each accepted call is a git commit that redeploys the app, and list_invoices reports the payment immediately.",
		false), func(ctx context.Context, _ *mcp.CallToolRequest, a setInvoicePaidArgs) (*mcp.CallToolResult, any, error) {
		return result(s.SetInvoicePaid(ctx, InvoicePaymentRequest{
			Invoice: a.Invoice, Paid: a.Paid, Date: a.Date, Note: a.Note, Reason: a.Reason,
		}))
	})

	mcp.AddTool(server, tool("get_finance_config",
		"The dated rules every figure on the finance pages is computed from: the legislation in force period by period — minimum wage, both contribution schedules, income tax, company profit tax and dividend tax — plus the salary plan and the target balance. Read this before explaining any figure rather than assuming a rate: a month before the earliest entry has nothing in force and is charged nothing, which is not the same as a rate of zero. READ ONLY, and deliberately so: this is deployment configuration, changed by editing config.json and redeploying. There is no tool that writes it, because a past payslip has to stay reproducible against the rates it was actually computed under.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
		return result(s.FinanceConfig(ctx))
	})

	mcp.AddTool(server, tool("get_director_loan",
		"What the company owes its owner at the end of one month, or what the owner owes the company — the running balance between them. It opens on the figure stated in accounts.json, accrues that month's net income (which includes a dividend, net of its dividend tax), and is settled by the lines you marked with a movement. Positive means the company owes the owner. A month before every stated figure answers known:false — that is 'nobody has said', not 'nothing is owed', and the fix is a reading in the data repo rather than a tool here. The loan itself feeds only the company's worth, but the crossings it is built from are not private to it: what reached the owner outside payroll — a draw, a distribution actually paid, money paid back in — also raises or lowers the Actual column of Available to spend, because it is money he has. The residual this figure reports is net salary the company accrued and never transferred. Read notes: they say when one transfer looks marked twice.",
		true), func(ctx context.Context, _ *mcp.CallToolRequest, a monthArgs) (*mcp.CallToolResult, any, error) {
		return result(s.DirectorLoanFor(ctx, a.Month))
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
			"Pass month with each edit — get_actuals and search_transactions both return it beside the id. It is optional: the fallback lookup does find a line added minutes ago, but it scans the months around now, which is slower and only covers a three-year window. "+
			"There is no base_sha: only the ids you name are touched, so someone else's change to another line merges rather than being lost. "+
			"If any id does not exist, nothing in that month is written and the missing ids come back. reason lands in the commit message.",
		false), func(ctx context.Context, _ *mcp.CallToolRequest, a editArgs) (*mcp.CallToolResult, any, error) {
		return result(s.EditTransactions(ctx, a.request()))
	})

	mcp.AddTool(server, tool("record_account_balance",
		"Record what an account really held when a month closed — the figure read off the bank. This is how an account's balance is brought up to date, and it re-anchors every month after it: the projection is replaced by the fact, rather than reconciled against it. "+
			"as_of IS AN END-OF-MONTH, END-OF-DAY DATE AND NOTHING ELSE. It is the CLOSING balance of that month: 2026-07-31 is the money left when July ended, so it is what AUGUST OPENS WITH and is usable in August immediately. It changes nothing about July itself. "+
			"MID-MONTH BALANCES ARE NOT ALLOWED and are refused — as_of must be the last day of its month (31, 30, or 28/29 in February). Half a month is not a closing balance: the rest of that month's spending has not happened yet, so filing it as the month's figure would open the next month with money that is already gone. If the user gives you a balance read on the 12th, do not round it to a month end — ask them to read it again when the month closes. "+
			"A month that has not ended yet is refused for the same reason: a balance is read, never projected. "+
			"The reading is APPENDED to that account's history and nothing is written over. A month that already has a reading is refused rather than replaced, because one month closes on one figure and a past month must keep the number that was true then; a wrong figure is repaired by a human in the data repo. "+
			"account must be spelled exactly as list_accounts spells it. This never creates an account: which pot it belongs to — company or private — decides which side of the payroll cascade the money sits on, and that is a decision, not a guess. "+
			"balance is euros at the end of that day, decimals allowed, negative for an overdrawn account. There is no base_sha: the reading is appended, so someone else's change merges rather than being lost. "+
			"Each accepted call is a git commit that redeploys the app, and the new figure is in force at once.",
		false), func(ctx context.Context, _ *mcp.CallToolRequest, a balanceArgs) (*mcp.CallToolResult, any, error) {
		return result(s.RecordAccountBalance(ctx, RecordBalanceRequest{
			Account: a.Account, AsOf: a.AsOf, Balance: a.Balance, Note: a.Note,
		}))
	})

	mcp.AddTool(server, tool("move_planned_expense",
		"Move one one-off cost to the month it was actually charged in, when get_reconciliation_status reports it as mistimed. Changes that category's date and nothing else — it cannot alter an amount, cannot touch anything that recurs monthly, and cannot move a category that is bounded to a from/until window. reason is required and lands in the commit message.",
		false), func(ctx context.Context, _ *mcp.CallToolRequest, a moveArgs) (*mcp.CallToolResult, any, error) {
		return result(s.MovePlannedExpense(ctx, MoveRequest{
			CategoryID: a.CategoryID, FromMonth: a.FromMonth, ToMonth: a.ToMonth,
			Reason: a.Reason, BaseSHA: a.BaseSHA,
		}))
	})

	mcp.AddTool(server, tool("schedule_amount_change",
		"Change the recurring price a budget category pays, from a future month on — the tool for a rent or subscription that rises next January. "+
			"from_month is YYYY-MM and must be in the future: a month that is already in force cannot be re-planned here, an already closed budget is fixed in budget.json. "+
			"Send amount (euros, decimals allowed) plus optionally minimal_amount to set the category's amount from that month on until a later change supersedes it; sending the same from_month again corrects the scheduled price. A correction replaces that month's entry as a whole, so a minimal_amount you do not resend is dropped. "+
			"Amounts are never negative, and minimal_amount cannot exceed the amount it reduces — both come back as a refusal. "+
			"Send remove instead of amount to undo a scheduled change for that month. "+
			"It refuses one-offs (a single price, full stop) and a change outside the category's own from/until window, which could never take effect. "+
			"Read the category's amount_changes from list_budget_categories or get_budget first — it is the whole scheduled list, so you know which months are already spoken for and which sha to base the edit on. "+
			"reason is required and lands in the commit message; each accepted call is a git commit that redeploys the app, and the new price is in force from the moment the change's month arrives.",
		false), func(ctx context.Context, _ *mcp.CallToolRequest, a amountChangeArgs) (*mcp.CallToolResult, any, error) {
		return result(s.ScheduleAmountChange(ctx, ScheduleAmountChangeRequest{
			CategoryID: a.CategoryID, FromMonth: a.FromMonth,
			Amount: a.Amount, MinimalAmount: a.MinimalAmount, Remove: a.Remove,
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
