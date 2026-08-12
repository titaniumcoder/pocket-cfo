package tracker

import (
	"fmt"
	"sort"
	"time"
)

// LegislationPeriod is the payroll law in force from a month: everything a
// government changes in one announcement, in one entry.
//
// Nested this way rather than one dated list per figure because that is how it
// arrives — a January package moves the minimum wage, the insurable ceiling
// and sometimes a contribution rate together, and splitting one announcement
// across four schedules is four chances to transcribe the date differently.
//
// Every figure is optional and carries forward: an entry states what changed,
// not what stayed. Repeating unchanged numbers in every entry is how one of
// them eventually gets repeated wrong.
type LegislationPeriod struct {
	From          yearMonth
	MinimumWage   *float64
	MaxInsurable  *float64
	EmployerRate  *float64
	EmployeeRate  *float64
	IncomeTaxRate *float64
}

// FromTheStart dates a period that has no start: the built-in defaults, which
// applied before anyone wrote anything down. Year one precedes every payroll
// month there will ever be.
var FromTheStart = yearMonth{1, time.January}

// Legislation is the periods in date order.
type Legislation []LegislationPeriod

// Rules are the figures resolved for one payroll month.
type Rules struct {
	MinimumEUR      float64 // statutory floor on gross salary; 0 = none
	MaxInsurableEUR float64 // ceiling on the contribution base; 0 = uncapped
	EmployerRate    float64
	EmployeeRate    float64
	IncomeTaxRate   float64
}

// LegislationEntry is one config.json entry. It lives here rather than in the
// config package so the parsing and the meaning stay together. The keys mirror
// the flat config.json names so there is one vocabulary, not two.
type LegislationEntry struct {
	From                      string   `json:"from"`
	MinimumWage               *float64 `json:"minimumWage"`
	SocialMaxInsurableMonthly *float64 `json:"socialMaxInsurableMonthly"`
	SocialEmployerRate        *float64 `json:"socialEmployerRate"`
	SocialEmployeeRate        *float64 `json:"socialEmployeeRate"`
	IncomeTaxRate             *float64 `json:"incomeTaxRate"`
}

// ParseLegislation turns config.json's entries into periods in date order.
// "2026-07" and "2026-07-01" are both accepted: the day is noise, since these
// figures apply to whole payroll months.
func ParseLegislation(entries []LegislationEntry) (Legislation, error) {
	out := make(Legislation, 0, len(entries))
	for i, e := range entries {
		ym, err := parseMonthOrDay(e.From)
		if err != nil {
			return nil, fmt.Errorf("legislation[%d]: from %q is not a month (2026-07) or a date (2026-07-01)", i, e.From)
		}
		p := LegislationPeriod{
			From: ym, MinimumWage: e.MinimumWage, MaxInsurable: e.SocialMaxInsurableMonthly,
			EmployerRate: e.SocialEmployerRate, EmployeeRate: e.SocialEmployeeRate,
			IncomeTaxRate: e.IncomeTaxRate,
		}
		if p.empty() {
			return nil, fmt.Errorf("legislation[%d] (%s) changes nothing — an entry that states no figure is a date nobody can act on", i, ym)
		}
		for name, v := range map[string]*float64{
			"minimumWage": p.MinimumWage, "socialMaxInsurableMonthly": p.MaxInsurable,
			"socialEmployerRate": p.EmployerRate, "socialEmployeeRate": p.EmployeeRate,
			"incomeTaxRate": p.IncomeTaxRate,
		} {
			if v != nil && *v < 0 {
				return nil, fmt.Errorf("legislation[%d] (%s): %s is %.4f, which is not a figure any legislature has published", i, ym, name, *v)
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From.ordinal() < out[j].From.ordinal() })
	for i := 1; i < len(out); i++ {
		if out[i].From == out[i-1].From {
			return nil, fmt.Errorf("legislation has two entries for %s — which one applies is then a coin toss", out[i].From)
		}
	}
	return out, nil
}

func (p LegislationPeriod) empty() bool {
	return p.MinimumWage == nil && p.MaxInsurable == nil && p.EmployerRate == nil &&
		p.EmployeeRate == nil && p.IncomeTaxRate == nil
}

// String renders a period the way config.json writes it, so what /info shows
// and what the file says can be compared by eye.
func (p LegislationPeriod) String() string {
	parts := []string{}
	for _, f := range []struct {
		name string
		v    *float64
	}{
		{"minimumWage", p.MinimumWage}, {"maxInsurable", p.MaxInsurable},
		{"employer", p.EmployerRate}, {"employee", p.EmployeeRate}, {"tax", p.IncomeTaxRate},
	} {
		if f.v != nil {
			parts = append(parts, fmt.Sprintf("%s %g", f.name, *f.v))
		}
	}
	when := fmt.Sprintf("%04d-%02d", p.From.Year, int(p.From.Month))
	if p.From == FromTheStart {
		when = "default"
	}
	return fmt.Sprintf("%s: %s", when, join(parts, ", "))
}

func join(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

func parseMonthOrDay(s string) (yearMonth, error) {
	for _, layout := range []string{"2006-01-02", "2006-01"} {
		if d, err := time.Parse(layout, s); err == nil {
			return yearMonth{d.Year(), d.Month()}, nil
		}
	}
	return yearMonth{}, fmt.Errorf("unparseable")
}

// rulesFor resolves the figures for a payroll month by applying every period
// that has taken effect, in order.
//
// Rates and the insurable ceiling also apply BACKWARDS from the earliest
// period. There is no such thing as a month with no income tax rate, so a
// month before anything was recorded uses the oldest figures on file — the
// best answer available. Without that, viewing a month before the first entry
// would show a salary with no deductions at all, which is not a subtle kind of
// wrong.
//
// The minimum wage is the exception and only ever applies forwards: no entry
// before July means there was no floor in June, because there was no job.
// Employment has a start date and a tax rate does not.
func (p PersonalParams) rulesFor(ym yearMonth) Rules {
	var r Rules
	if len(p.Legislation) > 0 {
		first := p.Legislation[0]
		setIf(&r.MaxInsurableEUR, first.MaxInsurable)
		setIf(&r.EmployerRate, first.EmployerRate)
		setIf(&r.EmployeeRate, first.EmployeeRate)
		setIf(&r.IncomeTaxRate, first.IncomeTaxRate)
	}
	for _, period := range p.Legislation {
		if period.From.ordinal() > ym.ordinal() {
			break
		}
		setIf(&r.MinimumEUR, period.MinimumWage)
		setIf(&r.MaxInsurableEUR, period.MaxInsurable)
		setIf(&r.EmployerRate, period.EmployerRate)
		setIf(&r.EmployeeRate, period.EmployeeRate)
		setIf(&r.IncomeTaxRate, period.IncomeTaxRate)
	}
	return r
}

func setIf(dst *float64, v *float64) {
	if v != nil {
		*dst = *v
	}
}
