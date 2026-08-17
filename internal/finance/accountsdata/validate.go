package accountsdata

import (
	"fmt"
	"time"
)

func ValidateAccounts(f AccountsFile) error {
	seenName := map[string]bool{}
	for _, a := range f.Accounts {
		if seenName[a.Name] {
			return fmt.Errorf("account name %q is used more than once — an account owns its whole history, so the readings have to live under one entry", a.Name)
		}
		seenName[a.Name] = true

		if err := readingProblems("account "+quoted(a.Name), a.Balances); err != nil {
			return err
		}
	}
	if f.DirectorLoan != nil {
		if err := readingProblems("the director's loan", f.DirectorLoan.Balances); err != nil {
			return err
		}
	}
	return nil
}

// readingProblems holds the rules a series of readings has to keep, applied
// to the accounts and to the director's loan from one place so the two cannot
// drift apart.
func readingProblems(what string, readings []Reading) error {
	if len(readings) == 0 {
		return fmt.Errorf("%s has no readings — an account is declared by its first balance, and one with none can never be given one", what)
	}

	seenMonth := map[string]string{}
	for _, r := range readings {
		d, err := time.Parse("2006-01-02", r.AsOf)
		if err != nil {
			return fmt.Errorf("%s has a reading with an invalid as_of %q", what, r.AsOf)
		}
		if !ClosesItsMonth(d) {
			return fmt.Errorf("%s has a reading dated %s, which is not the last day of its month — a reading is what a month CLOSED on, so it has to be dated %s or belong to another month. "+
				"A mid-month figure is not a closing figure: the rest of the month has not happened yet, and the month this one opens would start from somewhere already left behind",
				what, r.AsOf, LastDayOf(d).Format("2006-01-02"))
		}
		month := d.Format("2006-01")
		if other, ok := seenMonth[month]; ok {
			return fmt.Errorf("%s was read twice in %s (%s and %s) — one month closes on one figure, and the day is ignored", what, month, other, r.AsOf)
		}
		seenMonth[month] = r.AsOf
	}
	return nil
}

func quoted(s string) string { return fmt.Sprintf("%q", s) }
