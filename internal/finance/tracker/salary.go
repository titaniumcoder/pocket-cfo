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
	SalaryFixed   SalaryMode = "fixed"
)

type SalaryEntry struct {
	From   string   `json:"from"`
	To     string   `json:"to"`
	Mode   string   `json:"mode"`
	Amount *float64 `json:"amount"`
}

type SalaryPeriod struct {
	From      yearMonth
	To        yearMonth
	ToSet     bool
	Mode      SalaryMode
	AmountEUR float64
}

type SalaryDecision struct {
	Mode     SalaryMode
	FixedEUR float64
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
	case SalaryFull, SalaryMinimum, SalaryNone, SalaryFixed:
	case "":
		return SalaryPeriod{}, fmt.Errorf("salary[%d] has no mode — say full, minimum, fixed or none", i)
	default:
		return SalaryPeriod{}, fmt.Errorf("salary[%d] has mode %q, which is not full, minimum, fixed or none", i, e.Mode)
	}
	amount, err := parseSalaryAmount(i, mode, e.Amount)
	if err != nil {
		return SalaryPeriod{}, err
	}

	from, err := parseMonthOrDay(e.From)
	if err != nil {
		return SalaryPeriod{}, fmt.Errorf("salary[%d]: from %q is not a month (2026-04) or a date (2026-04-01)", i, e.From)
	}
	p := SalaryPeriod{From: from, Mode: mode, AmountEUR: amount}
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

func parseSalaryAmount(i int, mode SalaryMode, amount *float64) (float64, error) {
	if mode != SalaryFixed {
		if amount != nil {
			return 0, fmt.Errorf("salary[%d] states an amount with mode %q, which ignores it — only fixed pays a figure you name", i, mode)
		}
		return 0, nil
	}
	if amount == nil {
		return 0, fmt.Errorf("salary[%d] has mode fixed but names no amount — fixed is the mode for a gross monthly figure you choose, so the figure is the whole of it", i)
	}
	if *amount <= 0 {
		return 0, fmt.Errorf("salary[%d]: amount is %s, and a fixed salary of nothing is mode %q", i, formatNum(*amount), SalaryNone)
	}
	return *amount, nil
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

func (s SalaryPlan) decisionFor(ym yearMonth) SalaryDecision {
	for i, p := range s {
		if ym.ordinal() < p.From.ordinal() {
			break
		}
		if p.covers(ym) || p.runsOnThrough(ym, s.next(i)) {
			return SalaryDecision{Mode: p.Mode, FixedEUR: p.AmountEUR}
		}
	}
	return SalaryDecision{Mode: SalaryFull}
}

func (s SalaryPlan) modeFor(ym yearMonth) SalaryMode {
	return s.decisionFor(ym).Mode
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
	what := string(p.Mode)
	if p.Mode == SalaryFixed {
		what += " " + groupThousands(round(p.AmountEUR))
	}
	return when + ": " + what
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
		switch p.Mode {
		case SalaryMinimum:
			if err := requireMinimumWageThroughout(p, l, minimumWageMissing); err != nil {
				return err
			}
		case SalaryFixed:
			if err := requireFixedClearsTheMinimumWage(p, l); err != nil {
				return err
			}
		}
	}
	return nil
}

func minimumWageMissing(ym yearMonth) error {
	return fmt.Errorf("salary asks for the minimum in %s, but no minimumWage is in force then — that pays nothing, which is what mode %q is for", ym, SalaryNone)
}

func requireFixedClearsTheMinimumWage(p SalaryPeriod, l Legislation) error {
	return eachMonthOf(p, func(ym yearMonth) error {
		minimum := l.rulesAt(ym).MinimumEUR
		if minimum > 0 && p.AmountEUR < minimum {
			return fmt.Errorf("salary fixes %s for %s, below the %s minimum wage in force then. A fixed salary outranks what the company can afford, but not what the law sets — raise the amount, or say mode %q to track the minimum as it changes",
				groupThousands(round(p.AmountEUR)), ym, groupThousands(round(minimum)), SalaryMinimum)
		}
		return nil
	})
}

func requireMinimumWageThroughout(p SalaryPeriod, l Legislation, complain func(yearMonth) error) error {
	return eachMonthOf(p, func(ym yearMonth) error {
		if l.rulesAt(ym).MinimumEUR == 0 {
			return complain(ym)
		}
		return nil
	})
}

func eachMonthOf(p SalaryPeriod, check func(yearMonth) error) error {
	last := p.From
	if p.ToSet {
		last = p.To
	}
	for ym := p.From; ym.ordinal() <= last.ordinal(); ym = ym.addMonths(1) {
		if err := check(ym); err != nil {
			return err
		}
	}
	return nil
}
