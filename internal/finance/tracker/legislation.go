package tracker

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type Band struct {
	From float64 `json:"from"`
	Rate float64 `json:"rate"`
}

type Bands []Band

func (b Bands) on(base float64) float64 {
	total := 0.0
	for i, band := range b {
		if base <= band.From {
			break
		}
		upper := base
		if i+1 < len(b) && b[i+1].From < upper {
			upper = b[i+1].From
		}
		total += band.Rate * (upper - band.From)
	}
	return total
}

func (b Bands) String() string {
	if len(b) == 0 {
		return ""
	}
	parts := make([]string, 0, len(b))
	for i, band := range b {
		pct := formatNum(band.Rate*100) + "%"
		if i+1 < len(b) {
			parts = append(parts, pct+" to "+formatNum(b[i+1].From))
			continue
		}
		if i == 0 {
			parts = append(parts, pct)
		} else {
			parts = append(parts, "then "+pct)
		}
	}
	return strings.Join(parts, ", ")
}

func (b Bands) applied(base, minBase float64) string {
	if base <= 0 || len(b) == 0 {
		return ""
	}
	charged := base
	if minBase > charged {
		charged = minBase
	}

	var parts []string
	for i, band := range b {
		if charged <= band.From {
			break
		}
		pct := formatNum(band.Rate*100) + "%"
		last := i+1 == len(b) || charged <= b[i+1].From
		switch {
		case last && band.Rate == 0:
		case last && i == 0:
			parts = append(parts, pct)
		case last:
			parts = append(parts, "then "+pct)
		default:
			parts = append(parts, pct+" up to "+groupThousands(round(b[i+1].From)))
		}
	}
	out := strings.Join(parts, ", ")
	if minBase > base && out != "" {
		out += " on a " + groupThousands(round(minBase)) + " minimum base"
	}
	return out
}

type PartySchedule struct {
	MinBase *float64
	Bands   Bands
}

type LegislationPeriod struct {
	From             yearMonth
	MinimumWage      *float64
	Employer         *PartySchedule
	Employee         *PartySchedule
	IncomeTax        Bands
	CompanyProfitTax Bands
	DividendTax      Bands
}

type Legislation []LegislationPeriod

type PartyRules struct {
	MinBase float64
	Bands   Bands
}

type Rules struct {
	MinimumEUR       float64
	Employer         PartyRules
	Employee         PartyRules
	IncomeTax        Bands
	CompanyProfitTax Bands
	DividendTax      Bands
}

func (r Rules) nothingInForce() bool {
	return r.MinimumEUR == 0 && len(r.IncomeTax) == 0 &&
		r.Employer.MinBase == 0 && len(r.Employer.Bands) == 0 &&
		r.Employee.MinBase == 0 && len(r.Employee.Bands) == 0
}

type BandEntry struct {
	From float64  `json:"from"`
	Rate *float64 `json:"rate"`
}

type PartyEntry struct {
	MinBase *float64    `json:"minBase"`
	Bands   []BandEntry `json:"bands"`
}

type ContributionsEntry struct {
	Employer *PartyEntry `json:"employer"`
	Employee *PartyEntry `json:"employee"`
}

type TaxEntry struct {
	Bands []BandEntry `json:"bands"`
}

type LegislationEntry struct {
	From             string              `json:"from"`
	MinimumWage      *float64            `json:"minimumWage"`
	Contributions    *ContributionsEntry `json:"contributions"`
	IncomeTax        *TaxEntry           `json:"incomeTax"`
	CompanyProfitTax *TaxEntry           `json:"companyProfitTax"`
	DividendTax      *TaxEntry           `json:"dividendTax"`

	SocialMaxInsurableMonthly *float64 `json:"socialMaxInsurableMonthly"`
	SocialEmployerRate        *float64 `json:"socialEmployerRate"`
	SocialEmployeeRate        *float64 `json:"socialEmployeeRate"`
	IncomeTaxRate             *float64 `json:"incomeTaxRate"`
}

func ParseLegislation(entries []LegislationEntry) (Legislation, error) {
	out := make(Legislation, 0, len(entries))
	for i, e := range entries {
		if err := rejectRetiredKeys(i, e); err != nil {
			return nil, err
		}
		ym, err := parseMonthOrDay(e.From)
		if err != nil {
			return nil, fmt.Errorf("legislation[%d]: from %q is not a month (2026-07) or a date (2026-07-01)", i, e.From)
		}
		p := LegislationPeriod{From: ym, MinimumWage: e.MinimumWage}
		if e.MinimumWage != nil && *e.MinimumWage < 0 {
			return nil, fmt.Errorf("legislation[%d] (%s): minimumWage is %.4f, which is not a figure any legislature has published", i, ym, *e.MinimumWage)
		}
		if c := e.Contributions; c != nil {
			if c.Employer == nil && c.Employee == nil {
				return nil, fmt.Errorf("legislation[%d] (%s): contributions names neither employer nor employee, so it states nothing", i, ym)
			}
			if p.Employer, err = parseParty(i, ym, "employer", c.Employer); err != nil {
				return nil, err
			}
			if p.Employee, err = parseParty(i, ym, "employee", c.Employee); err != nil {
				return nil, err
			}
		}
		if p.IncomeTax, err = parseTax(i, ym, "incomeTax", e.IncomeTax); err != nil {
			return nil, err
		}
		if p.CompanyProfitTax, err = parseTax(i, ym, "companyProfitTax", e.CompanyProfitTax); err != nil {
			return nil, err
		}
		if p.DividendTax, err = parseTax(i, ym, "dividendTax", e.DividendTax); err != nil {
			return nil, err
		}
		if p.empty() {
			return nil, fmt.Errorf("legislation[%d] (%s) changes nothing — an entry that states no figure is a date nobody can act on", i, ym)
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

func rejectRetiredKeys(i int, e LegislationEntry) error {
	where := fmt.Sprintf("legislation[%d]", i)
	if e.SocialEmployerRate != nil || e.SocialEmployeeRate != nil {
		ceiling := ""
		if e.SocialMaxInsurableMonthly != nil {
			ceiling = fmt.Sprintf(", {\"from\": %s, \"rate\": 0}", formatNum(*e.SocialMaxInsurableMonthly))
		}
		party := func(name string, rate *float64) string {
			if rate == nil {
				return ""
			}
			return fmt.Sprintf("\n  \"contributions\": {\"%s\": {\"bands\": [{\"from\": 0, \"rate\": %s}%s]}}",
				name, formatRate(*rate), ceiling)
		}
		return fmt.Errorf("%s still uses socialEmployerRate/socialEmployeeRate/socialMaxInsurableMonthly, which are no longer read. "+
			"Contributions are per-party marginal bands now, and a ceiling is a band with a rate of zero:%s%s",
			where, party("employer", e.SocialEmployerRate), party("employee", e.SocialEmployeeRate))
	}
	if e.SocialMaxInsurableMonthly != nil {
		return fmt.Errorf("%s still uses socialMaxInsurableMonthly, which is no longer read: a ceiling is a band with a rate of zero, "+
			"e.g. \"bands\": [{\"from\": 0, \"rate\": 0.1892}, {\"from\": %s, \"rate\": 0}]", where, formatNum(*e.SocialMaxInsurableMonthly))
	}
	if e.IncomeTaxRate != nil {
		return fmt.Errorf("%s still uses incomeTaxRate, which is no longer read: write \"incomeTax\": {\"bands\": [{\"from\": 0, \"rate\": %s}]}",
			where, formatRate(*e.IncomeTaxRate))
	}
	return nil
}

func formatRate(v float64) string {
	return fmt.Sprintf("%g", v)
}

func parseParty(i int, ym yearMonth, name string, e *PartyEntry) (*PartySchedule, error) {
	if e == nil {
		return nil, nil
	}
	if e.MinBase == nil && len(e.Bands) == 0 {
		return nil, fmt.Errorf("legislation[%d] (%s): contributions.%s names neither minBase nor bands, so it states nothing", i, ym, name)
	}
	if e.MinBase != nil && *e.MinBase < 0 {
		return nil, fmt.Errorf("legislation[%d] (%s): contributions.%s.minBase is %.4f, which is not a figure any legislature has published", i, ym, name, *e.MinBase)
	}
	bands, err := parseBands(i, ym, "contributions."+name, e.Bands)
	if err != nil {
		return nil, err
	}
	return &PartySchedule{MinBase: e.MinBase, Bands: bands}, nil
}

func parseTax(i int, ym yearMonth, name string, e *TaxEntry) (Bands, error) {
	if e == nil {
		return nil, nil
	}
	bands, err := parseBands(i, ym, name, e.Bands)
	if err != nil {
		return nil, err
	}
	if bands == nil {
		return nil, fmt.Errorf("legislation[%d] (%s): %s names no bands, so it states nothing", i, ym, name)
	}
	return bands, nil
}

func parseBands(i int, ym yearMonth, where string, entries []BandEntry) (Bands, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	bands := make(Bands, 0, len(entries))
	for n, b := range entries {
		if b.Rate == nil {
			return nil, fmt.Errorf("legislation[%d] (%s): %s.bands[%d] has no rate — a band without one leaves that slice of the base uncharged by accident rather than on purpose", i, ym, where, n)
		}
		if *b.Rate < 0 || b.From < 0 {
			return nil, fmt.Errorf("legislation[%d] (%s): %s.bands[%d] is {from %.4f, rate %.4f}, which is not a figure any legislature has published", i, ym, where, n, b.From, *b.Rate)
		}
		if n == 0 && b.From != 0 {
			return nil, fmt.Errorf("legislation[%d] (%s): %s.bands starts at %.4f, leaving everything below it with no rate — the first band opens at 0", i, ym, where, b.From)
		}
		if n > 0 && b.From <= entries[n-1].From {
			return nil, fmt.Errorf("legislation[%d] (%s): %s.bands[%d] opens at %.4f, not above the %.4f before it — which band a base falls in is then a coin toss", i, ym, where, n, b.From, entries[n-1].From)
		}
		bands = append(bands, Band{From: b.From, Rate: *b.Rate})
	}
	return bands, nil
}

func (p LegislationPeriod) empty() bool {
	return p.MinimumWage == nil && p.Employer == nil && p.Employee == nil &&
		p.IncomeTax == nil && p.CompanyProfitTax == nil && p.DividendTax == nil
}

func (p LegislationPeriod) String() string {
	parts := []string{}
	if p.MinimumWage != nil {
		parts = append(parts, "minimumWage "+formatNum(*p.MinimumWage))
	}
	for _, f := range []struct {
		name string
		s    *PartySchedule
	}{{"employer", p.Employer}, {"employee", p.Employee}} {
		if f.s == nil {
			continue
		}
		if f.s.MinBase != nil {
			parts = append(parts, f.name+" minBase "+formatNum(*f.s.MinBase))
		}
		if f.s.Bands != nil {
			parts = append(parts, f.name+" "+f.s.Bands.String())
		}
	}
	for _, f := range []struct {
		name  string
		bands Bands
	}{{"tax", p.IncomeTax}, {"company profit tax", p.CompanyProfitTax}, {"dividend tax", p.DividendTax}} {
		if f.bands != nil {
			parts = append(parts, f.name+" "+f.bands.String())
		}
	}
	return fmt.Sprintf("%s: %s", p.From.configForm(), strings.Join(parts, ", "))
}

func parseMonthOrDay(s string) (yearMonth, error) {
	for _, layout := range []string{"2006-01-02", "2006-01"} {
		if d, err := time.Parse(layout, s); err == nil {
			return yearMonth{d.Year(), d.Month()}, nil
		}
	}
	return yearMonth{}, fmt.Errorf("unparseable")
}

func (p PersonalParams) rulesFor(ym yearMonth) Rules { return p.Legislation.rulesAt(ym) }

func (l Legislation) rulesAt(ym yearMonth) Rules {
	var r Rules
	for _, period := range l {
		if period.From.ordinal() > ym.ordinal() {
			break
		}
		setIf(&r.MinimumEUR, period.MinimumWage)
		applyParty(&r.Employer, period.Employer)
		applyParty(&r.Employee, period.Employee)
		if period.IncomeTax != nil {
			r.IncomeTax = period.IncomeTax
		}
		if period.CompanyProfitTax != nil {
			r.CompanyProfitTax = period.CompanyProfitTax
		}
		if period.DividendTax != nil {
			r.DividendTax = period.DividendTax
		}
	}
	return r
}

func applyParty(dst *PartyRules, s *PartySchedule) {
	if s == nil {
		return
	}
	setIf(&dst.MinBase, s.MinBase)
	if s.Bands != nil {
		dst.Bands = s.Bands
	}
}

func setIf(dst *float64, v *float64) {
	if v != nil {
		*dst = *v
	}
}
