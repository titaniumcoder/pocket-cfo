package tracker

import (
	"fmt"
	"sort"
	"strings"
)

type TargetEntry struct {
	From   string   `json:"from"`
	To     string   `json:"to"`
	Amount *float64 `json:"amount"`
}

type TargetPeriod struct {
	From      yearMonth
	To        yearMonth
	ToSet     bool
	AmountEUR float64
}

type TargetPlan []TargetPeriod

func ParseTargetPlan(entries []TargetEntry) (TargetPlan, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	plan := make(TargetPlan, 0, len(entries))
	for i, e := range entries {
		p, err := parseTargetEntry(i, e)
		if err != nil {
			return nil, err
		}
		plan = append(plan, p)
	}
	sort.Slice(plan, func(a, b int) bool { return plan[a].From.ordinal() < plan[b].From.ordinal() })
	if err := rejectTargetOverlaps(plan); err != nil {
		return nil, err
	}
	return plan, nil
}

func parseTargetEntry(i int, e TargetEntry) (TargetPeriod, error) {
	if e.Amount == nil {
		return TargetPeriod{}, fmt.Errorf("targetBalance[%d] names no amount — the amount is the whole of what a target says", i)
	}
	if *e.Amount <= 0 {
		return TargetPeriod{}, fmt.Errorf("targetBalance[%d]: amount is %s, and a target of nothing is always already met, so it would change no month", i, formatNum(*e.Amount))
	}
	from, err := parseMonthOrDay(e.From)
	if err != nil {
		return TargetPeriod{}, fmt.Errorf("targetBalance[%d]: from %q is not a month (2026-04) or a date (2026-04-01)", i, e.From)
	}
	p := TargetPeriod{From: from, AmountEUR: *e.Amount}
	if strings.TrimSpace(e.To) == "" {
		return p, nil
	}
	to, err := parseMonthOrDay(e.To)
	if err != nil {
		return TargetPeriod{}, fmt.Errorf("targetBalance[%d]: to %q is not a month (2026-06) or a date (2026-06-30)", i, e.To)
	}
	if to.ordinal() < from.ordinal() {
		return TargetPeriod{}, fmt.Errorf("targetBalance[%d]: to %s precedes from %s", i, to, from)
	}
	p.To, p.ToSet = to, true
	return p, nil
}

func rejectTargetOverlaps(plan TargetPlan) error {
	for i := 1; i < len(plan); i++ {
		prev, cur := plan[i-1], plan[i]
		if prev.From.ordinal() == cur.From.ordinal() {
			return fmt.Errorf("targetBalance states two figures for %s — the company cannot be saving towards both", cur.From)
		}
		if prev.ToSet && prev.To.ordinal() >= cur.From.ordinal() {
			return fmt.Errorf("targetBalance period from %s runs to %s, overlapping the one that starts %s", prev.From, prev.To, cur.From)
		}
	}
	return nil
}

func (t TargetPlan) at(ym yearMonth) (float64, bool) {
	for i, p := range t {
		if ym.ordinal() < p.From.ordinal() {
			break
		}
		if p.covers(ym) || p.runsOnThrough(ym, t.next(i)) {
			return p.AmountEUR, true
		}
	}
	return 0, false
}

func (t TargetPlan) next(i int) *TargetPeriod {
	if i+1 < len(t) {
		return &t[i+1]
	}
	return nil
}

func (p TargetPeriod) covers(ym yearMonth) bool {
	return p.ToSet && ym.ordinal() <= p.To.ordinal()
}

func (p TargetPeriod) runsOnThrough(ym yearMonth, next *TargetPeriod) bool {
	if p.ToSet {
		return false
	}
	return next == nil || ym.ordinal() < next.From.ordinal()
}

func (p TargetPeriod) String() string {
	when := p.From.configForm()
	if p.ToSet {
		when += "–" + p.To.configForm()
	}
	return when + ": " + groupThousands(round(p.AmountEUR))
}

// ValidateTargetAgainstSalary reports the months where a target is in force but
// cannot do anything, without refusing them. A target only ever downgrades a
// month that would otherwise pay a full salary — it has nothing to add to a
// month already told to pay the minimum, nothing, or a fixed figure. Writing
// one over such a month is allowed, because the salary block is the more
// explicit statement and the user is the authority, but silence would leave a
// setting that visibly does nothing and no way to find out why.
func ValidateTargetAgainstSalary(plan TargetPlan, salary SalaryPlan) []string {
	horizon, ok := salaryHorizon(salary)
	if !ok {
		return nil
	}
	var idle []string
	for _, p := range plan {
		last := horizon
		if p.ToSet && p.To.ordinal() < horizon.ordinal() {
			last = p.To
		}
		for ym := p.From; ym.ordinal() <= last.ordinal(); ym = ym.addMonths(1) {
			if mode := salary.modeFor(ym); mode != SalaryFull {
				idle = append(idle, fmt.Sprintf("%s (salary is %s then)", ym.configForm(), mode))
			}
		}
	}
	return idle
}

// salaryHorizon is the last month the salary block has anything to say about.
// An open-ended target runs forever, and past this month every month pays a
// full salary — which is exactly where a target does apply — so there is
// nothing to warn about beyond it and no need to walk to the end of time.
func salaryHorizon(salary SalaryPlan) (yearMonth, bool) {
	var last yearMonth
	found := false
	for _, p := range salary {
		end := p.From
		if p.ToSet {
			end = p.To
		}
		if !found || end.ordinal() > last.ordinal() {
			last, found = end, true
		}
	}
	return last, found
}

func RequireMinimumWageForTargets(plan TargetPlan, l Legislation) error {
	for _, p := range plan {
		period := SalaryPeriod{From: p.From, To: p.To, ToSet: p.ToSet}
		err := requireMinimumWageThroughout(period, l, func(ym yearMonth) error {
			return fmt.Errorf("targetBalance holds %s at the statutory minimum until the company reaches %s, but no minimumWage is in force then — the month would pay nothing at all",
				ym, groupThousands(round(p.AmountEUR)))
		})
		if err != nil {
			return err
		}
	}
	return nil
}
