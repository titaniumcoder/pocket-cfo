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

		seenMonth := map[string]string{}
		for _, r := range a.Balances {
			d, err := time.Parse("2006-01-02", r.AsOf)
			if err != nil {
				return fmt.Errorf("account %q has a balance with an invalid as_of %q", a.Name, r.AsOf)
			}
			month := d.Format("2006-01")
			if other, ok := seenMonth[month]; ok {
				return fmt.Errorf("account %q was read twice in %s (%s and %s) — one month closes on one figure, and the day is ignored", a.Name, month, other, r.AsOf)
			}
			seenMonth[month] = r.AsOf
		}
	}
	return nil
}
