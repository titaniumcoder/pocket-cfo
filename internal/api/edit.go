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

type Edit struct {
	ID    string `json:"id"`
	Month string `json:"month,omitempty"`

	Category  *string             `json:"category,omitempty"`
	Ignored   *string             `json:"ignored,omitempty"`
	Untracked *string             `json:"untracked,omitempty"`
	Splits    []actualsdata.Split `json:"splits,omitempty"`

	// Movement rides with ignored rather than being one of the four. An edit
	// that does not set it clears it, along with everything else the line
	// carried — re-attributing a transfer to a category must not leave the
	// marker behind, settling a loan against a line that is now groceries.
	Movement *actualsdata.Movement `json:"movement,omitempty"`
}

type EditRequest struct {
	Edits  []Edit `json:"edits"`
	Reason string `json:"reason,omitempty"`
}

type MonthEdit struct {
	Month  string `json:"month"`
	SHA    string `json:"sha"`
	Edited int    `json:"edited"`
}

type EditResult struct {
	Edited        int         `json:"edited"`
	Unchanged     []string    `json:"unchanged,omitempty"`
	Months        []MonthEdit `json:"months"`
	DeployPending bool        `json:"deploy_pending"`
}

func (s *Service) EditTransactions(ctx context.Context, req EditRequest) (*EditResult, error) {
	if len(req.Edits) == 0 {
		return nil, errorf(CodeInvalidRequest, "edits is required")
	}
	if s.Store == nil {
		return nil, errorf(CodeWriteNotConfigured, "writes are not configured")
	}

	seen := map[string]bool{}
	for i, e := range req.Edits {
		if strings.TrimSpace(e.ID) == "" {
			return nil, errorf(CodeInvalidRequest, "edit %d has no id", i+1)
		}
		if seen[e.ID] {
			return nil, errorf(CodeInvalidRequest, "edit %d names %s, which an earlier edit already changed — say what it should end up as, once", i+1, e.ID)
		}
		seen[e.ID] = true
		if err := checkDisposition(i, e); err != nil {
			return nil, err
		}
		if e.Month != "" {
			if _, _, err := ParseMonth(e.Month); err != nil {
				return nil, errorf(CodeInvalidRequest, "edit %d (%s): month %q must look like 2026-08", i+1, e.ID, e.Month)
			}
		}
	}

	byMonth, err := s.groupEditsByMonth(ctx, req.Edits)
	if err != nil {
		return nil, err
	}
	knownIDs, err := s.knownCategoryIDs(ctx)
	if err != nil {
		return nil, err
	}
	for i, e := range req.Edits {
		for _, id := range citedCategories(e) {
			if !knownIDs[id] {
				return nil, errorf(CodeValidationFailed,
					"edit %d (%s) cites category %q, which is not in budget.json — list_budget_categories has the legal ids", i+1, e.ID, id)
			}
		}
	}

	months := make([]string, 0, len(byMonth))
	for m := range byMonth {
		months = append(months, m)
	}
	sort.Strings(months)

	var planned []plannedEdit
	for _, month := range months {
		p, perr := s.planEdit(ctx, month, byMonth[month], knownIDs)
		if perr != nil {
			return nil, perr
		}
		planned = append(planned, p)
	}

	out := &EditResult{Months: []MonthEdit{}}
	for _, p := range planned {
		out.Edited += p.edited
		out.Unchanged = append(out.Unchanged, p.unchanged...)
		if p.edited == 0 {
			continue
		}
		sha, cerr := s.Store.Put(ctx, s.actualsPath(p.month), p.body, p.sha, editMessage(p, req.Reason))
		if cerr != nil {
			return nil, editCommitError(cerr, p.month, out.Months)
		}
		s.Actuals.Publish(p.month, p.body)
		out.Months = append(out.Months, MonthEdit{Month: p.month, SHA: sha, Edited: p.edited})
	}
	out.DeployPending = len(out.Months) > 0
	return out, nil
}

type plannedEdit struct {
	month     string
	sha       string
	body      []byte
	edited    int
	unchanged []string
}

func (s *Service) planEdit(ctx context.Context, month string, edits []Edit, knownIDs map[string]bool) (plannedEdit, error) {
	doc, _, sha, err := s.loadMonth(ctx, month)
	if err != nil {
		return plannedEdit{}, err
	}
	before := doc

	at := map[string]int{}
	for i, tx := range doc.Transactions {
		at[tx.Id] = i
	}

	var missing []string
	for _, e := range edits {
		if _, ok := at[e.ID]; !ok {
			missing = append(missing, e.ID)
		}
	}
	if len(missing) > 0 {
		return plannedEdit{}, &Error{
			Code:    CodeNotFound,
			Message: fmt.Sprintf("%s has no transaction %s — nothing in that month was changed", month, strings.Join(missing, ", ")),
			Details: map[string]any{"month": month, "missing": missing},
		}
	}

	doc.Transactions = append([]actualsdata.Transaction(nil), doc.Transactions...)
	p := plannedEdit{month: month, sha: sha}
	for _, e := range edits {
		i := at[e.ID]
		was := doc.Transactions[i]
		doc.Transactions[i] = applyEdit(was, e)
		if reflect.DeepEqual(was, doc.Transactions[i]) {
			p.unchanged = append(p.unchanged, e.ID)
			continue
		}
		p.edited++
	}

	if verr := actualsdata.ValidateActuals(doc, month, knownIDs); verr != nil {
		return plannedEdit{}, &Error{Code: CodeValidationFailed, Message: verr.Error()}
	}
	if derr := refuseDestruction(before, doc, true); derr != nil {
		return plannedEdit{}, derr
	}

	body, merr := marshalMonth(doc)
	if merr != nil {
		return plannedEdit{}, merr
	}
	p.body = body
	return p, nil
}

func applyEdit(tx actualsdata.Transaction, e Edit) actualsdata.Transaction {
	tx.Category, tx.Ignored, tx.Untracked, tx.Splits, tx.Movement = nil, nil, nil, nil, nil
	switch {
	case e.Category != nil:
		tx.Category = e.Category
	case e.Ignored != nil:
		tx.Ignored = e.Ignored
		tx.Movement = e.Movement
	case e.Untracked != nil:
		tx.Untracked = e.Untracked
	default:
		tx.Splits = e.Splits
	}
	return tx
}

func checkDisposition(i int, e Edit) error {
	if e.Movement != nil && e.Ignored == nil {
		return errorf(CodeInvalidRequest,
			"edit %d (%s): movement needs an ignored reason beside it — money crossing between you and the company is not a budget expense, so it says so like every other line that isn't", i+1, e.ID)
	}
	set := countSet(e.Category != nil, e.Ignored != nil, e.Untracked != nil, len(e.Splits) > 0)
	switch {
	case set == 0:
		return errorf(CodeInvalidRequest,
			"edit %d (%s) says nothing to change — set exactly one of category, ignored, untracked or splits", i+1, e.ID)
	case set > 1:
		return errorf(CodeInvalidRequest,
			"edit %d (%s) sets more than one of category, ignored, untracked and splits — a line carries exactly one", i+1, e.ID)
	}
	if e.Category != nil && *e.Category == "" {
		return errorf(CodeInvalidRequest, "edit %d (%s): category is empty — there is no way to leave a line undecided", i+1, e.ID)
	}
	if e.Ignored != nil && strings.TrimSpace(*e.Ignored) == "" {
		return errorf(CodeInvalidRequest, "edit %d (%s): ignored needs a reason, not a bare yes", i+1, e.ID)
	}
	if e.Untracked != nil && strings.TrimSpace(*e.Untracked) == "" {
		return errorf(CodeInvalidRequest, "edit %d (%s): untracked needs a note saying what it is waiting on", i+1, e.ID)
	}
	if len(e.Splits) == 1 {
		return errorf(CodeInvalidRequest, "edit %d (%s): one split is just a category — send two or more, or set category", i+1, e.ID)
	}
	return nil
}

func citedCategories(e Edit) []string {
	var out []string
	if e.Category != nil && *e.Category != "" {
		out = append(out, *e.Category)
	}
	for _, s := range e.Splits {
		if s.Category != nil && *s.Category != "" {
			out = append(out, *s.Category)
		}
	}
	return out
}

func countSet(flags ...bool) int {
	n := 0
	for _, f := range flags {
		if f {
			n++
		}
	}
	return n
}

func (s *Service) groupEditsByMonth(ctx context.Context, edits []Edit) (map[string][]Edit, error) {
	out := map[string][]Edit{}
	var index map[string]string
	for _, e := range edits {
		month := e.Month
		if month == "" {
			if index == nil {
				built, err := s.transactionMonths(ctx)
				if err != nil {
					return nil, err
				}
				index = built
			}
			found, ok := index[e.ID]
			if !ok {
				return nil, &Error{
					Code: CodeNotFound,
					Message: fmt.Sprintf("no transaction %s in the months on disk — pass month with the edit "+
						"(get_actuals and search_transactions return it), since a line recorded in the last few minutes is not deployed yet", e.ID),
					Details: map[string]string{"id": e.ID},
				}
			}
			month = found
		}
		out[month] = append(out[month], e)
	}
	return out, nil
}

func (s *Service) transactionMonths(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	now := s.now().Year()
	for year := now - 1; year <= now+1; year++ {
		for m := time.January; m <= time.December; m++ {
			af, present, err := s.Actuals.TransactionsForMonth(ctx, year, m)
			if err != nil {
				return nil, errorf(CodeInternal, "scanning %s: %v", monthKey(year, m), err)
			}
			if !present {
				continue
			}
			for _, tx := range af.Transactions {
				out[tx.Id] = af.Month
			}
		}
	}
	return out, nil
}

func editMessage(p plannedEdit, reason string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "fix(actuals): reattribute %s in %s\n\n", countLabel(p.edited, "transaction"), p.month)
	if r := strings.TrimSpace(reason); r != "" {
		b.WriteString(r)
		b.WriteString("\n")
	}
	return b.String()
}

func editCommitError(err error, month string, done []MonthEdit) error {
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
	return &Error{Code: CodeUpstream, Message: fmt.Sprintf("writing %s: %v", month, err), Details: details}
}
