package actualsdata

import (
	"errors"
	"fmt"
	"time"
)

// MonthKeyOf returns the "2026-08" key a data/actuals/ filename encodes.
func MonthKeyOf(name string) string {
	if len(name) < 7 {
		return ""
	}
	return name[:7]
}

// ValidateActuals checks what the JSON Schema can't express. A nil knownIDs
// skips the category cross-check, which is what the runtime loader does.
//
// Unlike ValidateBudget it reports every breach rather than the first: the
// file is machine-written in bulk, so one error per rerun is the wrong shape
// for a 200-line import.
func ValidateActuals(af ActualsFile, monthKey string, knownIDs map[string]bool) error {
	var problems []error

	if af.Month != monthKey {
		problems = append(problems, fmt.Errorf("month is %q but the filename says %q", af.Month, monthKey))
	}

	start, err := time.Parse("2006-01", af.Month)
	if err != nil {
		// Nothing else can be range-checked without a parseable month.
		return errors.Join(append(problems, fmt.Errorf("month %q is not a real month", af.Month))...)
	}
	end := start.AddDate(0, 1, -1)

	// "Nothing imported yet" is the absent file, not an empty one: a month
	// with transactions and no coverage claims to have read no days, which
	// makes the completeness check meaningless in the direction that matters.
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
		default:
			if from.Before(start) || to.After(end) {
				problems = append(problems, fmt.Errorf("coverage %d (%s): %s..%s reaches outside %s — a statement crossing a month boundary splits into two files", i+1, c.Account, c.From, c.To, af.Month))
			}
		}
		if _, err := parseDay(c.ImportedAt); err != nil {
			problems = append(problems, fmt.Errorf("coverage %d (%s): imported_at %q is not a real date", i+1, c.Account, c.ImportedAt))
		}
	}

	seen := map[string]bool{}
	for _, tx := range af.Transactions {
		if seen[tx.Id] {
			problems = append(problems, fmt.Errorf("transaction id %q appears more than once — ids must be unique so re-importing a statement is idempotent", tx.Id))
			continue
		}
		seen[tx.Id] = true

		hasCategory := tx.Category != nil && *tx.Category != ""
		hasIgnored := tx.Ignored != nil && *tx.Ignored != ""
		hasUntracked := tx.Untracked != nil && *tx.Untracked != ""
		hasSplits := len(tx.Splits) > 0
		dispositions := countTrue(hasCategory, hasIgnored, hasUntracked)
		switch {
		case hasSplits && dispositions > 0:
			problems = append(problems, fmt.Errorf("transaction %s has splits as well as a category, ignored or untracked reason — the parts decide, so the line itself must not", tx.Id))
		case dispositions > 1:
			problems = append(problems, fmt.Errorf("transaction %s has more than one of category, ignored and untracked — it must have exactly one", tx.Id))
		case dispositions == 0 && !hasSplits:
			// untracked counts as decided: it is the decision to decide later,
			// which is a different thing from a line nobody has looked at.
			problems = append(problems, fmt.Errorf("transaction %s has none of a category, an ignored reason, an untracked note or splits — no line may be left undecided", tx.Id))
		}
		problems = append(problems, splitProblems(tx, knownIDs)...)
		if hasCategory && knownIDs != nil && !knownIDs[*tx.Category] {
			problems = append(problems, fmt.Errorf("transaction %s cites category %q, which is not in budget.json", tx.Id, *tx.Category))
		}

		if tx.Amount == 0 {
			problems = append(problems, fmt.Errorf("transaction %s has an amount of 0 — record a line that isn't an expense with ignored instead", tx.Id))
		}

		day, err := parseDay(tx.Date)
		if err != nil {
			problems = append(problems, fmt.Errorf("transaction %s: date %q is not a real date", tx.Id, tx.Date))
			continue
		}
		if day.Before(start) || day.After(end) {
			problems = append(problems, fmt.Errorf("transaction %s is dated %s, outside %s", tx.Id, tx.Date, af.Month))
		}
	}

	return errors.Join(problems...)
}

// splitProblems checks the parts of a split line. The sum rule is the one
// that matters: a split whose parts do not add up to the line silently moves
// money into or out of the month's total, which no other check would notice.
//
// Compared in whole cents, since 33.33 + 33.33 + 33.34 is exactly 100 in the
// units the app actually counts in and not in binary floating point.
func splitProblems(tx Transaction, knownIDs map[string]bool) []error {
	if len(tx.Splits) == 0 {
		return nil
	}
	var problems []error
	for i, s := range tx.Splits {
		hasCategory := s.Category != nil && *s.Category != ""
		hasIgnored := s.Ignored != nil && *s.Ignored != ""
		hasUntracked := s.Untracked != nil && *s.Untracked != ""
		switch dispositions := countTrue(hasCategory, hasIgnored, hasUntracked); {
		case dispositions > 1:
			problems = append(problems, fmt.Errorf("transaction %s split %d has more than one of category, ignored and untracked — it must have exactly one", tx.Id, i+1))
		case dispositions == 0:
			problems = append(problems, fmt.Errorf("transaction %s split %d has none of a category, an ignored reason and an untracked note", tx.Id, i+1))
		case hasCategory && knownIDs != nil && !knownIDs[*s.Category]:
			problems = append(problems, fmt.Errorf("transaction %s split %d cites category %q, which is not in budget.json", tx.Id, i+1, *s.Category))
		}
		if s.Amount == 0 {
			problems = append(problems, fmt.Errorf("transaction %s split %d has an amount of 0 — leave it out instead", tx.Id, i+1))
		}
	}
	if got, want := roundCents(SplitSum(tx)), roundCents(tx.Amount); got != want {
		problems = append(problems, fmt.Errorf("transaction %s splits add up to %.2f, but the line is %.2f — a split that does not reconcile to the statement moves money nothing else would catch",
			tx.Id, float64(got)/100, float64(want)/100))
	}
	return problems
}

// countTrue is how the exactly-one rule is checked in one place for lines and
// parts alike, so a fourth disposition cannot be added to one and forgotten in
// the other.
func countTrue(flags ...bool) int {
	n := 0
	for _, f := range flags {
		if f {
			n++
		}
	}
	return n
}

func roundCents(euros float64) int {
	if euros < 0 {
		return -int(-euros*100 + 0.5)
	}
	return int(euros*100 + 0.5)
}

func parseDay(s string) (time.Time, error) {
	return time.Parse("2006-01-02", s)
}
