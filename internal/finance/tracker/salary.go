package tracker

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type SalaryMode string

const (
	SalaryFull    SalaryMode = "full"
	SalaryMinimum SalaryMode = "minimum"
	SalaryNone    SalaryMode = "none"
)

type SalaryEntry struct {
	From string `json:"from"`
	To   string `json:"to"`
	Mode string `json:"mode"`
}

type SalaryPeriod struct {
	From  yearMonth
	To    yearMonth
	ToSet bool
	Mode  SalaryMode
}

type SalaryPlan []SalaryPeriod

func ParseSalaryPlan(entries []SalaryEntry) (SalaryPlan, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	plan := make(SalaryPlan, 0, len(entries))
	for i, e := range entries {
		p, err := parseSalaryEntry(i, e)
		if err != nil {
			return nil, err
		}
		plan = append(plan, p)
	}
	sort.Slice(plan, func(a, b int) bool { return plan[a].From.ordinal() < plan[b].From.ordinal() })
	if err := rejectOverlaps(plan); err != nil {
		return nil, err
	}
	return plan, nil
}

func parseSalaryEntry(i int, e SalaryEntry) (SalaryPeriod, error) {
	mode := SalaryMode(strings.TrimSpace(e.Mode))
	switch mode {
	case SalaryFull, SalaryMinimum, SalaryNone:
	case "":
		return SalaryPeriod{}, fmt.Errorf("salary[%d] has no mode — say full, minimum or none", i)
	default:
		return SalaryPeriod{}, fmt.Errorf("salary[%d] has mode %q, which is not full, minimum or none", i, e.Mode)
	}

	from, err := parseMonthOrDay(e.From)
	if err != nil {
		return SalaryPeriod{}, fmt.Errorf("salary[%d]: from %q is not a month (2026-04) or a date (2026-04-01)", i, e.From)
	}
	p := SalaryPeriod{From: from, Mode: mode}
	if strings.TrimSpace(e.To) == "" {
		return p, nil
	}
	to, err := parseMonthOrDay(e.To)
	if err != nil {
		return SalaryPeriod{}, fmt.Errorf("salary[%d]: to %q is not a month (2026-06) or a date (2026-06-30)", i, e.To)
	}
	if to.ordinal() < from.ordinal() {
		return SalaryPeriod{}, fmt.Errorf("salary[%d]: to %s precedes from %s", i, to, from)
	}
	p.To, p.ToSet = to, true
	return p, nil
}

func rejectOverlaps(plan SalaryPlan) error {
	for i := 1; i < len(plan); i++ {
		prev, cur := plan[i-1], plan[i]
		if prev.From.ordinal() == cur.From.ordinal() {
			return fmt.Errorf("salary states two things about %s — one of them is wrong and nothing decides which", cur.From)
		}
		if prev.ToSet && prev.To.ordinal() >= cur.From.ordinal() {
			return fmt.Errorf("salary period from %s runs to %s, overlapping the one that starts %s", prev.From, prev.To, cur.From)
		}
	}
	return nil
}

func (s SalaryPlan) modeFor(ym yearMonth) SalaryMode {
	for i, p := range s {
		if ym.ordinal() < p.From.ordinal() {
			break
		}
		if p.covers(ym) || p.runsOnThrough(ym, s.next(i)) {
			return p.Mode
		}
	}
	return SalaryFull
}

func (s SalaryPlan) next(i int) *SalaryPeriod {
	if i+1 < len(s) {
		return &s[i+1]
	}
	return nil
}

func (p SalaryPeriod) covers(ym yearMonth) bool {
	return p.ToSet && ym.ordinal() <= p.To.ordinal()
}

func (p SalaryPeriod) runsOnThrough(ym yearMonth, next *SalaryPeriod) bool {
	if p.ToSet {
		return false
	}
	return next == nil || ym.ordinal() < next.From.ordinal()
}

func (p SalaryPeriod) String() string {
	when := p.From.configForm()
	if p.ToSet {
		when += "–" + p.To.configForm()
	}
	return when + ": " + string(p.Mode)
}

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

func ValidateSalaryAgainstLegislation(plan SalaryPlan, l Legislation) error {
	for _, p := range plan {
		if p.Mode != SalaryMinimum {
			continue
		}
		if err := requireMinimumWageThroughout(p, l); err != nil {
			return err
		}
	}
	return nil
}

func requireMinimumWageThroughout(p SalaryPeriod, l Legislation) error {
	last := p.From
	if p.ToSet {
		last = p.To
	}
	for ym := p.From; ym.ordinal() <= last.ordinal(); ym = ym.addMonths(1) {
		if l.rulesAt(ym).MinimumEUR == 0 {
			return fmt.Errorf("salary asks for the minimum in %s, but no minimumWage is in force then — that pays nothing, which is what mode %q is for", ym, SalaryNone)
		}
	}
	return nil
}
