package tracker

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// SalaryMode is what a month does about paying a salary at all.
//
// The cascade used to know only one story: pay the largest salary the company
// can afford, and raise it to the statutory minimum if it falls short. That is
// one decision out of three a one-person company actually makes. Choosing the
// minimum so the rest stays in the company, and paying nothing at all, are
// ordinary months, and neither was expressible.
type SalaryMode string

const (
	// SalaryFull is the default and the old behaviour: solve for what the
	// company can afford, floored by the statutory minimum.
	SalaryFull SalaryMode = "full"
	// SalaryMinimum pays exactly the statutory minimum however much was
	// affordable. The difference stays in the company, which is the point.
	SalaryMinimum SalaryMode = "minimum"
	// SalaryNone pays nothing: nobody was on the payroll that month, so
	// nothing is contributed or taxed either.
	SalaryNone SalaryMode = "none"
)

// SalaryEntry is one dated stretch of months, as config.json writes it.
//
// From is required; To is inclusive and optional. Without a To the entry runs
// until the next one begins — the carry-forward the legislation block already
// teaches, so the two read the same way. With one it ends there and the gap
// falls back to full, which is also what an absent block means everywhere.
type SalaryEntry struct {
	From string `json:"from"`
	To   string `json:"to"`
	Mode string `json:"mode"`
}

// SalaryPeriod is a parsed entry. ToSet distinguishes "ends here" from "runs
// on", which a zero yearMonth could not.
type SalaryPeriod struct {
	From  yearMonth
	To    yearMonth
	ToSet bool
	Mode  SalaryMode
}

// SalaryPlan is the periods in date order, non-overlapping.
type SalaryPlan []SalaryPeriod

// ParseSalaryPlan validates the salary block. Like ParseLegislation it is
// fail-fast and names the entry: this decides whether a person was paid, and a
// typo that silently means "no salary" is the failure worth refusing to boot
// over.
func ParseSalaryPlan(entries []SalaryEntry) (SalaryPlan, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	plan := make(SalaryPlan, 0, len(entries))
	for i, e := range entries {
		mode := SalaryMode(strings.TrimSpace(e.Mode))
		switch mode {
		case SalaryFull, SalaryMinimum, SalaryNone:
		case "":
			return nil, fmt.Errorf("salary[%d] has no mode — say full, minimum or none", i)
		default:
			return nil, fmt.Errorf("salary[%d] has mode %q, which is not full, minimum or none", i, e.Mode)
		}

		from, err := parseMonthOrDay(e.From)
		if err != nil {
			return nil, fmt.Errorf("salary[%d]: from %q is not a month (2026-04) or a date (2026-04-01)", i, e.From)
		}
		p := SalaryPeriod{From: from, Mode: mode}
		if strings.TrimSpace(e.To) != "" {
			to, terr := parseMonthOrDay(e.To)
			if terr != nil {
				return nil, fmt.Errorf("salary[%d]: to %q is not a month (2026-06) or a date (2026-06-30)", i, e.To)
			}
			if to.ordinal() < from.ordinal() {
				return nil, fmt.Errorf("salary[%d]: to %s precedes from %s", i, to, from)
			}
			p.To, p.ToSet = to, true
		}
		plan = append(plan, p)
	}

	sort.Slice(plan, func(a, b int) bool { return plan[a].From.ordinal() < plan[b].From.ordinal() })
	for i := 1; i < len(plan); i++ {
		prev, cur := plan[i-1], plan[i]
		if prev.From.ordinal() == cur.From.ordinal() {
			return nil, fmt.Errorf("salary states two things about %s — one of them is wrong and nothing decides which", cur.From)
		}
		if prev.ToSet && prev.To.ordinal() >= cur.From.ordinal() {
			return nil, fmt.Errorf("salary period from %s runs to %s, overlapping the one that starts %s", prev.From, prev.To, cur.From)
		}
	}
	return plan, nil
}

// modeFor is what the given month does about salary. Anything no period covers
// is full, so an absent block leaves every month exactly as it was.
func (s SalaryPlan) modeFor(ym yearMonth) SalaryMode {
	want := ym.ordinal()
	for i, p := range s {
		if want < p.From.ordinal() {
			break
		}
		switch {
		case p.ToSet:
			if want <= p.To.ordinal() {
				return p.Mode
			}
			// Ended before this month; a later period may still cover it.
		case i+1 < len(s) && want >= s[i+1].From.ordinal():
			// Open-ended, but the next period has taken over.
		default:
			return p.Mode
		}
	}
	return SalaryFull
}

// String renders one period the way config.json writes it, for /info — the
// numeric form LegislationPeriod.String uses, since that page exists to be
// compared against the file by eye.
func (p SalaryPeriod) String() string {
	when := p.From.configForm()
	if p.ToSet {
		when += "–" + p.To.configForm()
	}
	return when + ": " + string(p.Mode)
}

// ParseStartMonth reads a budgeting start month, as config.json writes it. The
// returned time is the first of that month; a blank string is no floor at all.
func ParseStartMonth(s string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, nil
	}
	ym, err := parseMonthOrDay(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("startMonth %q is not a month (2026-04) or a date (2026-04-01)", s)
	}
	return time.Date(ym.Year, ym.Month, 1, 0, 0, 0, 0, time.UTC), nil
}

// ValidateSalaryAgainstLegislation refuses a month set to the minimum when no
// minimum wage is in force for it.
//
// The two blocks are independent everywhere else, and that is the problem: on
// its own each one is fine, and together they mean a salary of zero for a
// month the file says should be paid. The page would show that as a zero and
// never say why, so it is caught at load like every other legal-obligation
// mistake in config.json.
//
// An open-ended period is checked only at its first month: a minimum wage
// carries forward once set, so if it is missing there it is missing for the
// whole stretch.
func ValidateSalaryAgainstLegislation(plan SalaryPlan, l Legislation) error {
	for _, p := range plan {
		if p.Mode != SalaryMinimum {
			continue
		}
		last := p.From
		if p.ToSet {
			last = p.To
		}
		for ym := p.From; ym.ordinal() <= last.ordinal(); ym = ym.addMonths(1) {
			if l.rulesAt(ym).MinimumEUR > 0 {
				continue
			}
			return fmt.Errorf("salary asks for the minimum in %s, but no minimumWage is in force then — that pays nothing, which is what mode \"none\" is for", ym)
		}
	}
	return nil
}
