package tracker

import (
	"sort"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
)

type Dividend struct {
	On        yearMonth
	Day       string
	AmountEUR float64
}

type Dividends []Dividend

// dividendDue is one month's distributions, already summed. Days is kept so
// the page can name the dates rather than presenting a total nobody can
// reconcile against the file.
type dividendDue struct {
	AmountEUR float64
	Days      []string
}

func dividendsIn(bf budgetdata.BudgetFile) Dividends {
	out := make(Dividends, 0, len(bf.Dividends))
	for _, entry := range bf.Dividends {
		day, err := time.Parse("2006-01-02", entry.Date)
		if err != nil {
			continue
		}
		out = append(out, Dividend{
			On:        yearMonth{day.Year(), day.Month()},
			Day:       entry.Date,
			AmountEUR: entry.Amount,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].On.ordinal() != out[j].On.ordinal() {
			return out[i].On.ordinal() < out[j].On.ordinal()
		}
		return out[i].Day < out[j].Day
	})
	return out
}

func (d Dividends) dueIn(ym yearMonth) dividendDue {
	var due dividendDue
	for _, entry := range d {
		if entry.On != ym {
			continue
		}
		due.AmountEUR += entry.AmountEUR
		due.Days = append(due.Days, entry.Day)
	}
	return due
}
