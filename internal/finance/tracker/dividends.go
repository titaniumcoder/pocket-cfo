package tracker

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
)

type Dividend struct {
	On        yearMonth
	Day       string
	AmountEUR float64
	Note      string
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
			Note:      derefNote(entry.Note),
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

func derefNote(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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

// DividendReport is one planned distribution with both taxes already worked
// out. The agent reads the arithmetic rather than recomputing it from rates
// it might resolve to the wrong month.
type DividendReport struct {
	Date                  string `json:"date"`
	GrossCents            int    `json:"gross_cents"`
	CompanyProfitTaxCents int    `json:"company_profit_tax_cents"`
	DividendTaxCents      int    `json:"dividend_tax_cents"`
	NetToOwnerCents       int    `json:"net_to_owner_cents"`
	CostToCompanyCents    int    `json:"cost_to_company_cents"`
	CashNeededCents       int    `json:"cash_needed_cents"`
	Note                  string `json:"note,omitempty"`
	Unrated               string `json:"unrated,omitempty"`
}

// DividendsIn reports the distributions a month holds, charged at the rates in
// force for it.
func (p PersonalParams) DividendsIn(d Dividends, year int, month time.Month) []DividendReport {
	ym := yearMonth{year, month}
	r := p.rulesFor(ym)
	var out []DividendReport
	for _, entry := range d {
		if entry.On != ym {
			continue
		}
		if r.CompanyProfitTax == nil || r.DividendTax == nil {
			out = append(out, DividendReport{
				Date:       entry.Day,
				GrossCents: round(toCent(entry.AmountEUR) * 100),
				Note:       entry.Note,
				Unrated: fmt.Sprintf("no company profit tax or dividend tax is in force in %s — config.json's legislation states no rate, so these taxes are unknown rather than zero",
					ym),
			})
			continue
		}

		gross := toCent(entry.AmountEUR)
		profitTax := round(toCent(r.CompanyProfitTax.on(gross)) * 100)
		dividendTax := round(toCent(r.DividendTax.on(gross)) * 100)
		grossCents := round(gross * 100)
		out = append(out, DividendReport{
			Date:                  entry.Day,
			GrossCents:            grossCents,
			CompanyProfitTaxCents: profitTax,
			DividendTaxCents:      dividendTax,
			NetToOwnerCents:       grossCents - dividendTax,
			CostToCompanyCents:    grossCents + profitTax,
			CashNeededCents:       profitTax + dividendTax,
			Note:                  entry.Note,
		})
	}
	return out
}

// DividendClearing sizes the distribution whose net exactly settles what the
// owner owes. The gross is larger than the debt because the dividend tax comes
// off on the way, and the cash the company must find is smaller than either —
// it is the two taxes, and nothing else.
func (p PersonalParams) DividendClearing(owedCents, year int, month time.Month) *DividendReport {
	if owedCents <= 0 {
		return nil
	}
	r := p.rulesFor(yearMonth{year, month})
	if r.CompanyProfitTax == nil || r.DividendTax == nil {
		return nil
	}
	owed := float64(owedCents) / 100
	// Solved rather than iterated: net(g) = g - dividendTax(g), and with a flat
	// band that inverts directly. Bands are walked in case it is not flat.
	gross := owed
	for range 8 {
		net := gross - toCent(r.DividendTax.on(gross))
		if math.Abs(net-owed) < 0.005 {
			break
		}
		gross += owed - net
	}
	gross = toCent(gross)
	profitTax := round(toCent(r.CompanyProfitTax.on(gross)) * 100)
	dividendTax := round(toCent(r.DividendTax.on(gross)) * 100)
	grossCents := round(gross * 100)
	return &DividendReport{
		GrossCents:            grossCents,
		CompanyProfitTaxCents: profitTax,
		DividendTaxCents:      dividendTax,
		NetToOwnerCents:       grossCents - dividendTax,
		CostToCompanyCents:    grossCents + profitTax,
		CashNeededCents:       profitTax + dividendTax,
	}
}
