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
	return nil
}
