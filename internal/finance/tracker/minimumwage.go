package tracker

import (
	"fmt"
	"sort"
	"time"
)

// MinimumWagePeriod is a statutory minimum monthly gross salary, in force from
// From until the next period starts.
//
// A list rather than one figure, because a minimum wage is legislation and
// legislation changes — Bulgaria's has risen every January for years. One
// number would mean either editing it each time the law changes and losing
// what last year's figures were computed against, or a schedule kept somewhere
// outside the app where nothing checks it.
//
// The earliest period is also the start of employment: before it there is no
// floor, because there was no job.
type MinimumWagePeriod struct {
	From      yearMonth
	AmountEUR float64
}

// ParseMinimumWage turns config.json's entries into periods, sorted so lookup
// is a scan from the end. "2026-07" and "2026-07-01" are both accepted: the day
// is noise here, since a wage floor applies to whole payroll months.
func ParseMinimumWage(entries []MinimumWageEntry) ([]MinimumWagePeriod, error) {
	out := make([]MinimumWagePeriod, 0, len(entries))
	for i, e := range entries {
		ym, err := parseMonthOrDay(e.From)
		if err != nil {
			return nil, fmt.Errorf("minimumWage[%d]: from %q is not a month (2026-07) or a date (2026-07-01)", i, e.From)
		}
		if e.Amount < 0 {
			return nil, fmt.Errorf("minimumWage[%d]: amount %.2f is negative", i, e.Amount)
		}
		out = append(out, MinimumWagePeriod{From: ym, AmountEUR: e.Amount})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From.ordinal() < out[j].From.ordinal() })
	for i := 1; i < len(out); i++ {
		if out[i].From == out[i-1].From {
			return nil, fmt.Errorf("minimumWage has two entries for %s — which one applies is then a coin toss", out[i].From)
		}
	}
	return out, nil
}

// String renders a period the way config.json writes it, so what /info shows
// and what the file says can be compared by eye.
func (p MinimumWagePeriod) String() string {
	return fmt.Sprintf("from %04d-%02d: %.2f", p.From.Year, int(p.From.Month), p.AmountEUR)
}

// MinimumWageEntry is one config.json entry. It lives here rather than in the
// config package so the parsing and the meaning stay together.
type MinimumWageEntry struct {
	From   string  `json:"from"`
	Amount float64 `json:"amount"`
}

func parseMonthOrDay(s string) (yearMonth, error) {
	for _, layout := range []string{"2006-01-02", "2006-01"} {
		if d, err := time.Parse(layout, s); err == nil {
			return yearMonth{d.Year(), d.Month()}, nil
		}
	}
	return yearMonth{}, fmt.Errorf("unparseable")
}

// minimumFor is the floor in force for a month: the latest period that has
// started. Zero before the first one, which is what makes "not employed yet"
// and "not configured" the same harmless answer.
func (p PersonalParams) minimumFor(ym yearMonth) float64 {
	var amount float64
	for _, period := range p.MinimumWage {
		if period.From.ordinal() <= ym.ordinal() {
			amount = period.AmountEUR
			continue
		}
		break
	}
	return amount
}
