package budgetdata

import (
	"fmt"
	"strings"
	"time"
)

// validateDate parses a YYYY-MM-DD date, returning a descriptive error if it
// doesn't parse (the schema only checks the pattern shape, not calendar
// validity, e.g. "2026-02-30").
func validateDate(field, date string) error {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return fmt.Errorf("field %s: invalid date %q", field, date)
	}
	return nil
}

// ValidateBudget checks what the JSON Schema can't express: category names
// are unique within their own group (not across the whole file — a category
// is always shown nested under its group header, so "Hotel" under both
// "Company - Vienna Trip" and "Galati Trips" is unambiguous on the page; only
// a repeat within the same group is actually a mistake), every category has
// a positive amount (a zero-euro line is almost certainly a mistake — note
// this only applies to the base amount; an override may still be 0, to skip
// a specific month), a dated category's date is a real calendar date, a
// category's url (if any) is an http(s) link — not javascript: or another
// scheme, since it's rendered directly as an <a href> — a category's
// minimal_amount (if any) doesn't exceed its amount, a category's overrides
// (if any) are real calendar dates with no duplicate months and non-negative
// amounts, and loan names are unique.
func ValidateBudget(f BudgetFile) error {
	for _, g := range f.Groups {
		seen := map[string]bool{}
		for _, c := range g.Categories {
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
