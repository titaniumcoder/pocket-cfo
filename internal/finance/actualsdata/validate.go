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
		switch {
		case hasCategory && hasIgnored:
			problems = append(problems, fmt.Errorf("transaction %s has both a category and an ignored reason — it must have exactly one", tx.Id))
		case !hasCategory && !hasIgnored:
			problems = append(problems, fmt.Errorf("transaction %s has neither a category nor an ignored reason — no line may be left undecided", tx.Id))
		case hasCategory && knownIDs != nil && !knownIDs[*tx.Category]:
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

func parseDay(s string) (time.Time, error) {
	return time.Parse("2006-01-02", s)
}
