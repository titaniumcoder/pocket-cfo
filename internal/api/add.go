package api

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
)

// AddRequest is a batch of statement lines to record, and the coverage that
// says which days they were read from.
//
// Deliberately no month: the caller sends what it read off a statement, and
// each line's own date decides which file it lands in. That a month is a file
// is this app's business, not the caller's, and a statement that crosses a
// month boundary is one import rather than two conversations.
type AddRequest struct {
	Transactions []actualsdata.Transaction `json:"transactions"`
	Coverage     []actualsdata.Coverage    `json:"coverage"`
}

// MonthWrite is one month this call committed.
type MonthWrite struct {
	Month string `json:"month"`
	SHA   string `json:"sha"`
	Added int    `json:"added"`
}

// AddResult reports what landed where.
type AddResult struct {
	Added         int          `json:"added"`
	Skipped       []string     `json:"skipped,omitempty"`
	Months        []MonthWrite `json:"months"`
	Unchanged     []string     `json:"unchanged_months,omitempty"`
	DeployPending bool         `json:"deploy_pending"`
}

// AddTransactions records statement lines that are not in the file yet.
//
// It cannot remove anything, and not by policy: the merge only appends to a
// slice it just read, and refuseDestruction proves that of its own output
// before committing. That is what lets it drop base_sha — an optimistic lock
// existed to stop a stale whole-month document erasing lines it never saw, and
// there is no whole-month document here.
//
// New lines go at the end rather than sorted into place, so the commit shows
// what was added and nothing else — which is the property a reviewer needs to
// confirm at a glance.
func (s *Service) AddTransactions(ctx context.Context, req AddRequest) (*AddResult, error) {
	if len(req.Transactions) == 0 && len(req.Coverage) == 0 {
		return nil, errorf(CodeInvalidRequest, "send at least one transaction or coverage range")
	}
	if s.Store == nil {
		return nil, errorf(CodeWriteNotConfigured, "writes are not configured")
	}

	txByMonth, err := groupTransactionsByMonth(req.Transactions)
	if err != nil {
		return nil, err
	}
	covByMonth, err := groupCoverageByMonth(req.Coverage)
	if err != nil {
		return nil, err
	}

	knownIDs, err := s.knownCategoryIDs(ctx)
	if err != nil {
		return nil, err
	}
	// Checked here as well as in ValidateActuals, because a mistyped category
	// is the most likely thing to be wrong about a batch and this is the last
	// point before the first network call.
	if err := checkCategories(req.Transactions, knownIDs); err != nil {
		return nil, err
	}

	// Every month is merged and validated before any of them is committed, so
	// a batch that is wrong about its second month does not leave the first
	// one written.
	var planned []plannedMonth
	for _, month := range monthsIn(txByMonth, covByMonth) {
		p, perr := s.planAdd(ctx, month, txByMonth[month], covByMonth[month], knownIDs)
		if perr != nil {
			return nil, perr
		}
		planned = append(planned, p)
	}

	out := &AddResult{Months: []MonthWrite{}}
	for _, p := range planned {
		out.Added += p.added
		out.Skipped = append(out.Skipped, p.skipped...)
		if !p.changed {
			// Nothing to say: re-sending a statement that is already recorded
			// should be a no-op, not an empty commit and a redeploy.
			out.Unchanged = append(out.Unchanged, p.month)
			continue
		}
		msg := fmt.Sprintf("feat(actuals): add %s to %s", countLabel(p.added, "transaction"), p.month)
		if p.added == 0 {
			msg = fmt.Sprintf("feat(actuals): extend coverage of %s", p.month)
		}
		sha, cerr := s.Store.Put(ctx, s.actualsPath(p.month), p.body, p.sha, msg+"\n")
		if cerr != nil {
			return nil, commitError(cerr, p.month, out.Months)
		}
		out.Months = append(out.Months, MonthWrite{Month: p.month, SHA: sha, Added: p.added})
	}
	out.DeployPending = len(out.Months) > 0
	return out, nil
}

// plannedMonth is one month's merged document, ready to commit.
type plannedMonth struct {
	month   string
	sha     string
	body    []byte
	added   int
	skipped []string
	changed bool
}

func (s *Service) planAdd(ctx context.Context, month string, txs []actualsdata.Transaction, cov []actualsdata.Coverage, knownIDs map[string]bool) (plannedMonth, error) {
	doc, _, sha, err := s.loadMonth(ctx, month)
	if err != nil {
		return plannedMonth{}, err
	}
	before := doc

	existing := map[string]actualsdata.Transaction{}
	for _, tx := range doc.Transactions {
		existing[tx.Id] = tx
	}

	p := plannedMonth{month: month, sha: sha}
	for _, tx := range txs {
		was, seen := existing[tx.Id]
		if seen {
			// The id is derived from account+date+amount+description, so the
			// same statement read twice produces the same ids — which only
			// helps if re-sending it is a no-op rather than a duplicate.
			if reflect.DeepEqual(was, tx) {
				p.skipped = append(p.skipped, tx.Id)
				continue
			}
			return plannedMonth{}, &Error{
				Code: CodeConflict,
				Message: fmt.Sprintf("transaction %s is already recorded in %s and differs from what you sent — "+
					"use edit_transactions to change a recorded line", tx.Id, month),
				Details: map[string]string{"id": tx.Id, "month": month},
			}
		}
		existing[tx.Id] = tx
		doc.Transactions = append(doc.Transactions, tx)
		p.added++
	}
	doc.Coverage = mergeCoverage(doc.Coverage, cov)

	if doc.Month == "" {
		doc.Month = month
	}
	// Said plainly here, because "which days did you read" is a question the
	// caller can answer and a schema error about minItems is not.
	if len(doc.Coverage) == 0 {
		return plannedMonth{}, errorf(CodeInvalidRequest,
			"%s has never been reconciled, so it needs a coverage range saying which days you read before anything can be filed under it", month)
	}
	if verr := actualsdata.ValidateActuals(doc, month, knownIDs); verr != nil {
		return plannedMonth{}, &Error{Code: CodeValidationFailed, Message: verr.Error()}
	}
	if derr := refuseDestruction(before, doc, false); derr != nil {
		return plannedMonth{}, derr
	}

	body, merr := marshalMonth(doc)
	if merr != nil {
		return plannedMonth{}, merr
	}
	p.body = body
	// Compared as documents, not as bytes: a file that happens to be formatted
	// by hand would otherwise look changed, and re-sending a statement already
	// recorded would commit a pure reformat and redeploy the app for nothing.
	p.changed = !reflect.DeepEqual(before, doc)
	return p, nil
}

// mergeCoverage folds new ranges into the recorded ones. A range for the same
// account and start day replaces its predecessor — that is a re-import reading
// further into the month — and anything else is appended. Nothing is dropped,
// and the recorded order is left alone so the diff shows only what moved.
//
// Coverage only ever grows. Re-sending a shorter range for the same start is
// a claim to have read fewer days than are already recorded, which is the
// coverage equivalent of deleting transactions: it would silently reopen a
// month the dashboard has already stopped withholding judgement on. The wider
// range wins rather than the request being refused, since the usual cause is
// a re-import of a statement that was already read further.
func mergeCoverage(have, incoming []actualsdata.Coverage) []actualsdata.Coverage {
	out := append([]actualsdata.Coverage(nil), have...)
	for _, c := range incoming {
		replaced := false
		for i, existing := range out {
			if existing.Account != c.Account || existing.From != c.From {
				continue
			}
			if c.To >= existing.To {
				out[i] = c
			}
			replaced = true
			break
		}
		if !replaced {
			out = append(out, c)
		}
	}
	return out
}

func groupTransactionsByMonth(txs []actualsdata.Transaction) (map[string][]actualsdata.Transaction, error) {
	out := map[string][]actualsdata.Transaction{}
	seen := map[string]bool{}
	for i, tx := range txs {
		// A real date, not just the right shape: 2026-13-01 matches the
		// pattern, and left alone it would send us looking for a month that
		// cannot exist — a round trip to GitHub to learn what the calendar
		// already knew.
		if err := checkDay(tx.Date); err != nil {
			return nil, errorf(CodeInvalidRequest, "transaction %d (%s): date %q %s", i+1, tx.Id, tx.Date, err)
		}
		if strings.TrimSpace(tx.Id) == "" {
			return nil, errorf(CodeInvalidRequest, "transaction %d has no id", i+1)
		}
		if seen[tx.Id] {
			return nil, errorf(CodeInvalidRequest, "transaction id %q appears twice in this request", tx.Id)
		}
		seen[tx.Id] = true
		out[monthOf(tx.Date)] = append(out[monthOf(tx.Date)], tx)
	}
	return out, nil
}

func groupCoverageByMonth(cov []actualsdata.Coverage) (map[string][]actualsdata.Coverage, error) {
	out := map[string][]actualsdata.Coverage{}
	for i, c := range cov {
		if err := checkDay(c.From); err != nil {
			return nil, errorf(CodeInvalidRequest, "coverage %d (%s): from %q %s", i+1, c.Account, c.From, err)
		}
		if err := checkDay(c.To); err != nil {
			return nil, errorf(CodeInvalidRequest, "coverage %d (%s): to %q %s", i+1, c.Account, c.To, err)
		}
		if monthOf(c.From) != monthOf(c.To) {
			return nil, errorf(CodeInvalidRequest,
				"coverage %d (%s): %s..%s crosses a month boundary — split it into one range per month, since a month is one file",
				i+1, c.Account, c.From, c.To)
		}
		out[monthOf(c.From)] = append(out[monthOf(c.From)], c)
	}
	return out, nil
}

// monthsIn returns every month either map mentions, oldest first, so a batch
// spanning a boundary writes in the order a human would read it.
func monthsIn(a map[string][]actualsdata.Transaction, b map[string][]actualsdata.Coverage) []string {
	set := map[string]bool{}
	for k := range a {
		set[k] = true
	}
	for k := range b {
		set[k] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// commitError says which months already landed. A batch spanning two months is
// two commits, so a failure on the second leaves the first written — pretending
// otherwise would send the caller looking for data that is already there.
func commitError(err error, month string, done []MonthWrite) error {
	var landed []string
	for _, m := range done {
		landed = append(landed, m.Month)
	}
	details := map[string]any{"failed_month": month}
	if len(landed) > 0 {
		details["already_committed"] = landed
	}
	if e, ok := err.(*Error); ok {
		return &Error{Code: e.Code, Message: e.Message, Details: details}
	}
	return &Error{
		Code:    CodeUpstream,
		Message: fmt.Sprintf("writing %s: %v", month, err),
		Details: details,
	}
}

// checkDay insists on a date the calendar agrees with, not merely one shaped
// like a date. The returned text completes "date %q ...".
func checkDay(day string) error {
	if !dayRE.MatchString(day) {
		return fmt.Errorf("must look like 2026-08-14")
	}
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return fmt.Errorf("is not a real date")
	}
	return nil
}

// checkCategories rejects a category no budget knows about before anything is
// read or written, so the common typo costs no round trip.
func checkCategories(txs []actualsdata.Transaction, knownIDs map[string]bool) error {
	for i, tx := range txs {
		for _, p := range actualsdata.PartsOf(tx) {
			if p.Category != "" && !knownIDs[p.Category] {
				return errorf(CodeValidationFailed,
					"transaction %d (%s) cites category %q, which is not in budget.json — list_budget_categories has the legal ids", i+1, tx.Id, p.Category)
			}
		}
	}
	return nil
}

func countLabel(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
