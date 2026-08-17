package tracker

import (
	"fmt"
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

// dividendDue is one month's distributions, already summed. The entries are
// kept so the page can name the dates rather than presenting a total nobody
// can reconcile against the file.
type dividendDue struct {
	AmountEUR float64
	Entries   []Dividend
}

var noDividend dividendDue

func (d dividendDue) none() bool { return d.AmountEUR == 0 }

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

// unrated is the one thing a dividend cannot be charged through: a month
// stating no rate. Everywhere else in this module an un-legislated month is
// charged nothing and says so on the page, because a salary happens in every
// month the navigation offers and refusing them all would break the page. A
// dividend exists only where somebody deliberately wrote one, so the refusal
// is contained to that month — and charging 0% and 0% would be a wrong figure
// wearing a right one's clothes.
func (d dividendDue) unrated(ym yearMonth, r Rules) string {
	if d.none() {
		return ""
	}
	for _, missing := range []struct {
		name  string
		bands Bands
	}{{"companyProfitTax", r.CompanyProfitTax}, {"dividendTax", r.DividendTax}} {
		if missing.bands == nil {
			return fmt.Sprintf("%s pays a dividend of %s, but no %s is in force then — config.json's legislation states no rate, and charging none would be a guess rather than a rule.",
				ym, formatEuro(round(d.AmountEUR*100)), missing.name)
		}
	}
	return ""
}

// DividendRow is one distribution under a month that holds more than one, so
// the total on the page can be read back against the entries in budget.json.
type DividendRow struct {
	Day   string
	Cents int
}

func (d dividendDue) dividendRows() []DividendRow {
	rows := make([]DividendRow, 0, len(d.Entries))
	for _, entry := range d.Entries {
		rows = append(rows, DividendRow{Day: formatDay(entry.Day), Cents: round(entry.AmountEUR * 100)})
	}
	return rows
}

func (d Dividends) dueIn(ym yearMonth) dividendDue {
	var due dividendDue
	for _, entry := range d {
		if entry.On != ym {
			continue
		}
		due.AmountEUR += entry.AmountEUR
		due.Entries = append(due.Entries, entry)
	}
	return due
}
