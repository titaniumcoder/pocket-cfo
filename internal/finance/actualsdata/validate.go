package actualsdata

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

func MonthKeyOf(name string) string {
	if len(name) < 7 {
		return ""
	}
	return name[:7]
}

func ValidateActuals(af ActualsFile, monthKey string, knownIDs map[string]bool) error {
	var problems []error
	if af.Month != monthKey {
		problems = append(problems, fmt.Errorf("month is %q but the filename says %q", af.Month, monthKey))
	}

	first, err := time.Parse("2006-01", af.Month)
	if err != nil {
		return errors.Join(append(problems, fmt.Errorf("month %q is not a real month", af.Month))...)
	}
	last := first.AddDate(0, 1, -1)

	problems = append(problems, coverageProblems(af, first, last)...)
	problems = append(problems, transactionProblems(af, first, last, knownIDs)...)
	return errors.Join(problems...)
}

func coverageProblems(af ActualsFile, first, last time.Time) []error {
	var problems []error
	if len(af.Coverage) == 0 {
		problems = append(problems, fmt.Errorf("%s has no coverage — a month must say which days were read", af.Month))
	}
	for i, c := range af.Coverage {
		from, ferr := parseDay(c.From)
		to, terr := parseDay(c.To)
		switch {
		case ferr != nil:
			problems = append(problems, fmt.Errorf("coverage %d (%s): from %q is not a real date", i+1, c.Account, c.From))
		case terr != nil:
			problems = append(problems, fmt.Errorf("coverage %d (%s): to %q is not a real date", i+1, c.Account, c.To))
		case to.Before(from):
			problems = append(problems, fmt.Errorf("coverage %d (%s): to %s precedes from %s", i+1, c.Account, c.To, c.From))
		case from.Before(first) || to.After(last):
			problems = append(problems, fmt.Errorf("coverage %d (%s): %s..%s reaches outside %s — a statement crossing a month boundary splits into two files", i+1, c.Account, c.From, c.To, af.Month))
		}
		if _, err := parseDay(c.ImportedAt); err != nil {
			problems = append(problems, fmt.Errorf("coverage %d (%s): imported_at %q is not a real date", i+1, c.Account, c.ImportedAt))
		}
	}
	return problems
}

func transactionProblems(af ActualsFile, first, last time.Time, knownIDs map[string]bool) []error {
	var problems []error
	seen := map[string]bool{}
	for i, tx := range af.Transactions {
		if tx.Id == "" {
			problems = append(problems, fmt.Errorf("transaction %d (%s, %s) has no id — the id is what makes a repeat import a no-op instead of a duplicate", i+1, tx.Date, tx.Description))
			continue
		}
		if seen[tx.Id] {
			problems = append(problems, fmt.Errorf("transaction id %q appears more than once — ids must be unique so re-importing a statement is idempotent", tx.Id))
			continue
		}
		seen[tx.Id] = true

		problems = appendIfProblem(problems, dispositionProblem(tx))
		problems = append(problems, movementProblems(tx)...)
		problems = append(problems, splitProblems(tx, knownIDs)...)
		problems = appendIfProblem(problems, unknownCategoryProblem(tx, knownIDs))
		problems = appendIfProblem(problems, zeroAmountProblem(tx))
		problems = appendIfProblem(problems, dateProblem(tx, af.Month, first, last))
	}
	return problems
}

func dispositionProblem(tx Transaction) error {
	set := countTrue(hasText(tx.Category), hasText(tx.Ignored), hasText(tx.Untracked))
	switch {
	case len(tx.Splits) > 0 && set > 0:
		return fmt.Errorf("transaction %s has splits as well as a category, ignored or untracked reason — the parts decide, so the line itself must not", tx.Id)
	case set > 1:
		return fmt.Errorf("transaction %s has more than one of category, ignored and untracked — it must have exactly one", tx.Id)
	case set == 0 && len(tx.Splits) == 0:
		return fmt.Errorf("transaction %s has none of a category, an ignored reason, an untracked note or splits — no line may be left undecided", tx.Id)
	}
	return nil
}

func unknownCategoryProblem(tx Transaction, knownIDs map[string]bool) error {
	if !hasText(tx.Category) || knownIDs == nil || knownIDs[*tx.Category] {
		return nil
	}
	return fmt.Errorf("transaction %s cites category %q, which is not in budget.json", tx.Id, *tx.Category)
}

func zeroAmountProblem(tx Transaction) error {
	if tx.Amount != 0 {
		return nil
	}
	return fmt.Errorf("transaction %s has an amount of 0 — record a line that isn't an expense with ignored instead", tx.Id)
}

func dateProblem(tx Transaction, monthKey string, first, last time.Time) error {
	day, err := parseDay(tx.Date)
	if err != nil {
		return fmt.Errorf("transaction %s: date %q is not a real date", tx.Id, tx.Date)
	}
	if day.Before(first) || day.After(last) {
		return fmt.Errorf("transaction %s is dated %s, outside %s", tx.Id, tx.Date, monthKey)
	}
	return nil
}

func splitProblems(tx Transaction, knownIDs map[string]bool) []error {
	if len(tx.Splits) == 0 {
		return nil
	}
	var problems []error
	for i, s := range tx.Splits {
		problems = appendIfProblem(problems, splitDispositionProblem(tx.Id, i, s, knownIDs))
		if s.Amount == 0 {
			problems = append(problems, fmt.Errorf("transaction %s split %d has an amount of 0 — leave it out instead", tx.Id, i+1))
		}
	}
	return appendIfProblem(problems, splitsReconcileProblem(tx))
}

func splitDispositionProblem(id string, i int, s Split, knownIDs map[string]bool) error {
	switch set := countTrue(hasText(s.Category), hasText(s.Ignored), hasText(s.Untracked)); {
	case set > 1:
		return fmt.Errorf("transaction %s split %d has more than one of category, ignored and untracked — it must have exactly one", id, i+1)
	case set == 0:
		return fmt.Errorf("transaction %s split %d has none of a category, an ignored reason and an untracked note", id, i+1)
	case hasText(s.Category) && knownIDs != nil && !knownIDs[*s.Category]:
		return fmt.Errorf("transaction %s split %d cites category %q, which is not in budget.json", id, i+1, *s.Category)
	}
	return nil
}

// movementProblems keeps the marker orthogonal to the disposition rule: it
// rides beside an ignored reason rather than replacing one, so a marked line
// is still a line that says why it is not a budget expense.
func movementProblems(tx Transaction) []error {
	var problems []error
	if tx.Movement != nil {
		problems = appendIfProblem(problems, unknownMovementProblem(tx.Id, "", *tx.Movement))
		if len(tx.Splits) > 0 {
			problems = append(problems, fmt.Errorf("transaction %s is marked as a %s and also has splits — the parts decide, so the line itself must not", tx.Id, *tx.Movement))
		} else if !hasText(tx.Ignored) {
			problems = append(problems, fmt.Errorf("transaction %s is marked as a %s but carries no ignored reason — money crossing between you and the company is not a budget expense, so it says so like every other line that isn't", tx.Id, *tx.Movement))
		}
		problems = appendIfProblem(problems, directionProblem(tx.Id, "", *tx.Movement, tx.Amount))
	}
	for i, s := range tx.Splits {
		if s.Movement == nil {
			continue
		}
		where := fmt.Sprintf(" split %d", i+1)
		problems = appendIfProblem(problems, unknownMovementProblem(tx.Id, where, *s.Movement))
		if !hasText(s.Ignored) {
			problems = append(problems, fmt.Errorf("transaction %s%s is marked as a %s but carries no ignored reason", tx.Id, where, *s.Movement))
		}
		problems = appendIfProblem(problems, directionProblem(tx.Id, where, *s.Movement, s.Amount))
	}
	return problems
}

func unknownMovementProblem(id, where string, m Movement) error {
	if KnownMovement(m) {
		return nil
	}
	return fmt.Errorf("transaction %s%s is marked %q, which is not a movement — use one of %s",
		id, where, string(m), strings.Join(Movements(), ", "))
}

// directionProblem is what enforces "record it once, on the company
// statement". Both statements are imported, so the same transfer arrives
// twice; the mirror has the opposite sign, so requiring the company side's
// sign makes the mirror unmarkable. It gets that guarantee without resolving
// account against accounts.json, which this file deliberately does not do.
func directionProblem(id, where string, m Movement, amount float64) error {
	in := m == MovementOwnerContribution
	if in == (amount < 0) {
		return nil
	}
	if in {
		return fmt.Errorf("transaction %s%s is marked owner_contribution but the amount is money leaving the company — money you put in arrives on the company statement as a credit. This is the mirror line on your private statement; mark the company side instead, once",
			id, where)
	}
	return fmt.Errorf("transaction %s%s is marked %s but the amount is money arriving — %s is money leaving the company. This is the mirror line on your private statement; mark the company side instead, once",
		id, where, m, m)
}

func splitsReconcileProblem(tx Transaction) error {
	got, want := roundCents(SplitSum(tx)), roundCents(tx.Amount)
	if got == want {
		return nil
	}
	return fmt.Errorf("transaction %s splits add up to %.2f, but the line is %.2f — a split that does not reconcile to the statement moves money nothing else would catch",
		tx.Id, float64(got)/100, float64(want)/100)
}

func appendIfProblem(problems []error, err error) []error {
	if err == nil {
		return problems
	}
	return append(problems, err)
}

func hasText(s *string) bool { return s != nil && *s != "" }

func countTrue(flags ...bool) int {
	n := 0
	for _, f := range flags {
		if f {
			n++
		}
	}
	return n
}

func RoundCents(euros float64) int { return roundCents(euros) }

func roundCents(euros float64) int {
	if euros < 0 {
		return -int(-euros*100 + 0.5)
	}
	return int(euros*100 + 0.5)
}

func parseDay(s string) (time.Time, error) {
	return time.Parse("2006-01-02", s)
}
