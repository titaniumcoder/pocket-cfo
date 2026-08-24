package budgetdata

import (
	"fmt"
	"strings"
	"time"
)

func validateDate(field, date string) error {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return fmt.Errorf("field %s: invalid date %q", field, date)
	}
	return nil
}

// monthOrdinal returns a comparable integer (YYYY*12+M) for the month a date
// names, so two bounds can be ordered by year-and-month, ignoring the day, the
// way the rest of the module reads a window month.
func monthOrdinal(date string) int {
	d, _ := time.Parse("2006-01-02", date)
	return d.Year()*12 + int(d.Month())
}

// validateAmountChanges lets the deliberate case through and refuses the
// copy-paste, the way the overrides check does: a change is a price in force
// from a month on, so it needs a parseable, non-duplicated month, and a
// minimal_amount that does not exceed its own period's amount.
func validateAmountChanges(c Category) error {
	if len(c.AmountChanges) == 0 {
		return nil
	}
	if c.Date != nil {
		return fmt.Errorf("category %q has both a one-off date and amount_changes — a one-off is a single price, full stop", c.Name)
	}
	seen := map[string]bool{}
	for _, ch := range c.AmountChanges {
		d, err := time.Parse("2006-01-02", ch.From)
		if err != nil {
			return fmt.Errorf("field category amount_changes: invalid date %q", ch.From)
		}
		month := d.Format("2006-01")
		if seen[month] {
			return fmt.Errorf("category %q has two amount_changes entries for %s (day is ignored) — which amount is in force then is a coin toss", c.Name, month)
		}
		seen[month] = true
		if ch.MinimalAmount != nil && *ch.MinimalAmount > ch.Amount {
			return fmt.Errorf("category %q's amount_changes entry for %s has a minimal_amount greater than its own amount", c.Name, month)
		}
		if c.From != nil && monthOrdinal(ch.From) < monthOrdinal(*c.From) {
			return fmt.Errorf("category %q's amount_changes entry for %s starts before its own from %s, so it could never take effect", c.Name, month, *c.From)
		}
		if c.Until != nil && monthOrdinal(ch.From) > monthOrdinal(*c.Until) {
			return fmt.Errorf("category %q's amount_changes entry for %s starts after its own until %s, so it could never take effect", c.Name, month, *c.Until)
		}
	}
	return nil
}

func ValidateBudget(f BudgetFile) error {
	categoryClaimingID := map[string]string{}
	for _, g := range f.Groups {
		seen := map[string]bool{}
		for _, c := range g.Categories {
			if c.Id == "" {
				return fmt.Errorf("category %q in group %q has no id", c.Name, g.Name)
			}
			if owner, ok := categoryClaimingID[c.Id]; ok {
				return fmt.Errorf("category id %q is used by both %q and %q — an id must be unique across the whole file", c.Id, owner, c.Name)
			}
			categoryClaimingID[c.Id] = c.Name
			if seen[c.Name] {
				return fmt.Errorf("category name %q is used more than once in group %q", c.Name, g.Name)
			}
			seen[c.Name] = true
			if c.Amount <= 0 {
				return fmt.Errorf("category %q has no positive amount", c.Name)
			}
			if c.MinimalAmount != nil && *c.MinimalAmount > c.Amount {
				return fmt.Errorf("category %q has a minimal_amount greater than its amount", c.Name)
			}
			seenOverride := map[string]bool{}
			for _, ov := range c.Overrides {
				d, err := time.Parse("2006-01-02", ov.Month)
				if err != nil {
					return fmt.Errorf("field category overrides: invalid month %q", ov.Month)
				}
				month := d.Format("2006-01")
				if seenOverride[month] {
					return fmt.Errorf("category %q has a duplicate overrides entry for %s (day is ignored)", c.Name, month)
				}
				seenOverride[month] = true
				if ov.Amount < 0 {
					return fmt.Errorf("category %q has an overrides entry for %s with a negative amount", c.Name, month)
				}
			}
			if c.Date != nil {
				if err := validateDate("category date", *c.Date); err != nil {
					return err
				}
				if c.From != nil || c.Until != nil {
					return fmt.Errorf("category %q has both a one-off date and a from/until window — a cost is either planned once in one month or recurring inside a window, never both", c.Name)
				}
			}
			if c.From != nil {
				if err := validateDate("category from", *c.From); err != nil {
					return err
				}
			}
			if c.Until != nil {
				if err := validateDate("category until", *c.Until); err != nil {
					return err
				}
			}
			if c.From != nil && c.Until != nil && monthOrdinal(*c.From) > monthOrdinal(*c.Until) {
				return fmt.Errorf("category %q has from %s after its until %s", c.Name, *c.From, *c.Until)
			}
			if verr := validateAmountChanges(c); verr != nil {
				return verr
			}
			if c.Url != nil && !strings.HasPrefix(*c.Url, "http://") && !strings.HasPrefix(*c.Url, "https://") {
				return fmt.Errorf("category %q has a url that isn't http(s): %q", c.Name, *c.Url)
			}
		}
	}
	seenLoan := map[string]bool{}
	for _, l := range f.Loans {
		if seenLoan[l.Name] {
			return fmt.Errorf("loan name %q is used more than once", l.Name)
		}
		seenLoan[l.Name] = true
	}
	return dividendProblems(f.Dividends)
}

// dividendProblems refuses the copy-paste and lets the deliberate case
// through: two distributions in one month sum, because summing them is the
// only reading, but the same amount on the same day twice is the one shape
// where summing silently doubles a real payout.
func dividendProblems(dividends []Dividend) error {
	seen := map[string]bool{}
	for _, d := range dividends {
		if err := validateDate("dividend date", d.Date); err != nil {
			return err
		}
		if d.Amount <= 0 {
			return fmt.Errorf("dividend on %s has no positive amount — a distribution of nothing is not one", d.Date)
		}
		key := fmt.Sprintf("%s|%v", d.Date, d.Amount)
		if seen[key] {
			return fmt.Errorf("two dividends of %v on %s — if that really is two distributions, date them apart or write one entry of %v", d.Amount, d.Date, d.Amount*2)
		}
		seen[key] = true
	}
	return nil
}
