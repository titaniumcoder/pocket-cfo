package tracker

import (
	"sort"
	"strings"
	"time"
)

type RuleChange struct {
	Anchor  string
	Label   string
	Changes []string
	Current bool
	Rules   []RuleRow
	Salary  string
	Target  string
	Notes   []string
}

type RuleRow struct {
	Name    string
	Value   string
	Changed bool
	Since   string
}

const (
	changeLegislation = "legislation"
	changeSalary      = "salary"
	changeTarget      = "target balance"
	changeStartMonth  = "start month"
)

func RulesTimeline(p PersonalParams, startMonth, today time.Time) []RuleChange {
	changes := map[yearMonth][]string{}
	note := func(ym yearMonth, what string) {
		for _, seen := range changes[ym] {
			if seen == what {
				return
			}
		}
		changes[ym] = append(changes[ym], what)
	}
	for _, period := range p.Legislation {
		note(period.From, changeLegislation)
	}
	for _, period := range p.Salary {
		note(period.From, changeSalary)
		if period.ToSet {
			note(period.To.addMonths(1), changeSalary)
		}
	}
	for _, period := range p.Target {
		note(period.From, changeTarget)
		if period.ToSet {
			note(period.To.addMonths(1), changeTarget)
		}
	}
	if !startMonth.IsZero() {
		note(yearMonth{startMonth.Year(), startMonth.Month()}, changeStartMonth)
	}

	months := make([]yearMonth, 0, len(changes))
	for ym := range changes {
		months = append(months, ym)
	}
	sort.Slice(months, func(i, j int) bool { return months[i].ordinal() < months[j].ordinal() })

	idle := idleNotesByMonth(p)
	out := make([]RuleChange, 0, len(months))
	for i, ym := range months {
		sort.Strings(changes[ym])
		next := yearMonth{}
		if i+1 < len(months) {
			next = months[i+1]
		}
		out = append(out, RuleChange{
			Anchor:  "rules-" + ym.configForm(),
			Label:   ym.String(),
			Changes: orderedChanges(changes[ym]),
			Rules:   ruleRows(p.Legislation, ym),
			Salary:  salaryInForce(p.Salary, ym),
			Target:  targetInForce(p.Target, ym),
			Notes:   notesBetween(idle, ym, next),
		})
	}
	markCurrent(out, months, yearMonth{today.Year(), today.Month()})
	return out
}

func orderedChanges(kinds []string) []string {
	order := []string{changeStartMonth, changeLegislation, changeSalary, changeTarget}
	out := make([]string, 0, len(kinds))
	for _, want := range order {
		for _, k := range kinds {
			if k == want {
				out = append(out, k)
			}
		}
	}
	return out
}

func markCurrent(entries []RuleChange, months []yearMonth, now yearMonth) {
	for i := len(months) - 1; i >= 0; i-- {
		if months[i].ordinal() <= now.ordinal() {
			entries[i].Current = true
			return
		}
	}
}

func ruleRows(l Legislation, ym yearMonth) []RuleRow {
	r := l.rulesAt(ym)
	rows := []RuleRow{
		{Name: "Minimum wage", Value: amountOrNone(r.MinimumEUR)},
		{Name: "Employer contributions", Value: partyRules(r.Employer)},
		{Name: "Employee contributions", Value: partyRules(r.Employee)},
		{Name: "Income tax", Value: bandsOrNone(r.IncomeTax)},
		{Name: "Company profit tax", Value: bandsOrNone(r.CompanyProfitTax)},
		{Name: "Dividend tax", Value: bandsOrNone(r.DividendTax)},
	}
	stated := []func(LegislationPeriod) bool{
		func(p LegislationPeriod) bool { return p.MinimumWage != nil },
		func(p LegislationPeriod) bool { return p.Employer != nil },
		func(p LegislationPeriod) bool { return p.Employee != nil },
		func(p LegislationPeriod) bool { return p.IncomeTax != nil },
		func(p LegislationPeriod) bool { return p.CompanyProfitTax != nil },
		func(p LegislationPeriod) bool { return p.DividendTax != nil },
	}
	for i := range rows {
		since, ok := l.lastStated(ym, stated[i])
		switch {
		case ok && since == ym:
			rows[i].Changed = true
		case ok:
			rows[i].Since = since.String()
		}
	}
	return rows
}

func (l Legislation) lastStated(ym yearMonth, stated func(LegislationPeriod) bool) (yearMonth, bool) {
	var found yearMonth
	ok := false
	for _, period := range l {
		if period.From.ordinal() > ym.ordinal() {
			break
		}
		if stated(period) {
			found, ok = period.From, true
		}
	}
	return found, ok
}

func amountOrNone(v float64) string {
	if v <= 0 {
		return "none"
	}
	return groupThousands(round(v))
}

func bandsOrNone(b Bands) string {
	if len(b) == 0 {
		return "none"
	}
	return b.String()
}

func partyRules(r PartyRules) string {
	if len(r.Bands) == 0 && r.MinBase <= 0 {
		return "none"
	}
	out := bandsOrNone(r.Bands)
	if r.MinBase > 0 {
		out += ", on a " + groupThousands(round(r.MinBase)) + " minimum base"
	}
	return out
}

func salaryInForce(plan SalaryPlan, ym yearMonth) string {
	for i, p := range plan {
		if ym.ordinal() < p.From.ordinal() {
			break
		}
		if p.covers(ym) || p.runsOnThrough(ym, plan.next(i)) {
			what := string(p.Mode)
			if p.Mode == SalaryFixed {
				what += " " + groupThousands(round(p.AmountEUR))
			}
			return what + " — " + periodLabel(p.From, p.To, p.ToSet)
		}
	}
	return "full (nothing configured)"
}

func targetInForce(plan TargetPlan, ym yearMonth) string {
	for i, p := range plan {
		if ym.ordinal() < p.From.ordinal() {
			break
		}
		if p.covers(ym) || p.runsOnThrough(ym, plan.next(i)) {
			return groupThousands(round(p.AmountEUR)) + " — " + periodLabel(p.From, p.To, p.ToSet)
		}
	}
	return "none"
}

func periodLabel(from, to yearMonth, toSet bool) string {
	if toSet {
		return from.String() + " to " + to.String()
	}
	return "from " + from.String()
}

func idleNotesByMonth(p PersonalParams) map[yearMonth]string {
	out := map[yearMonth]string{}
	for _, idle := range ValidateTargetAgainstSalary(p.Target, p.Salary) {
		if ym, err := parseMonthOrDay(strings.SplitN(idle, " ", 2)[0]); err == nil {
			out[ym] = idle
		}
	}
	return out
}

func notesBetween(idle map[yearMonth]string, from, next yearMonth) []string {
	var out []string
	for ym := from; ; ym = ym.addMonths(1) {
		if next != (yearMonth{}) && ym.ordinal() >= next.ordinal() {
			break
		}
		note, ok := idle[ym]
		if !ok {
			if next == (yearMonth{}) {
				break
			}
			continue
		}
		out = append(out, "The target does nothing in "+note+".")
	}
	return out
}
