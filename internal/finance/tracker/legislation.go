package tracker

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Band is a slice of a base and the rate charged on it. Bands are MARGINAL: a
// rate applies only to the part of the base that falls inside its own band, and
// From is where that band opens.
//
// This is what lets a ceiling be an ordinary band with a rate of zero rather
// than its own concept. "Contributions stop at the ceiling" is one special case
// of a rate changing at a threshold, not the general rule — UK employee NI
// drops from 8% to 2% above the Upper Earnings Limit instead of stopping, and
// UK employer NI never stops at all.
type Band struct {
	From float64 `json:"from"`
	Rate float64 `json:"rate"`
}

// Bands is one party's schedule, ascending and opening at zero so that no base
// is ever left with no rate.
type Bands []Band

// on returns what this schedule charges on base: each rate applied to the width
// of its own band, and to nothing else.
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

// String renders a schedule the way a reader would say it out loud — "18.92% to
// 2111.64, then 0%" for Bulgaria, "0% to 12570, 8% to 50270, then 2%" for UK
// employee NI. Unpunctuated on purpose: /info exists to be compared against
// config.json by eye, and the file has no thousands separators either.
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

// applied describes what this schedule actually charged on a base of this
// size — "18.92%" for a salary under the ceiling, "18.92% up to 2,112" for one
// above it — for the row that reports the money it charged.
//
// This replaces a blended charged-over-base percentage, which above a ceiling
// is a number that appears in no law and no config file: 18.92% up to 2,112 on
// a salary of 10,000 came out at 4%, and 4% is not a rate anyone set. What a
// reader wants from that column is the rate that was used and the boundary
// that bound it.
//
// A band the base passed clean through names the boundary that stopped it. The
// band the base came to rest inside names no boundary, because none applied.
// A trailing zero-rate band is dropped: a ceiling charges nothing above itself
// and "up to 2,112" has already said where it stopped — but a real top rate
// (UK employee NI at 2% above the upper limit) is not zero and still shows.
//
// minBase is what the charge was actually levied on when the base fell below
// it, which is the same misreporting in the other direction: the money was
// charged on 933, not on the 400 that was paid, so the row says so.
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
			// The ceiling itself. The band below already named the boundary.
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

// PartySchedule is one party's contribution rules as an entry states them.
// Both fields carry forward independently: an entry that gives only new bands
// keeps the minBase already in force.
//
// A band list is one indivisible statement, so giving Bands replaces the whole
// list rather than patching it — there is no sensible way to merge "the
// legislature published a new schedule" into an old one band at a time.
//
// Bands lives inside this struct rather than being the party itself so that a
// sliding scale — Germany's Übergangsbereich, France's réduction générale — can
// arrive later as a sibling field. Those are formulas, not flat bands, and no
// amount of band nesting expresses them; leaving the room costs nothing now and
// is the awkward thing to retrofit.
type PartySchedule struct {
	// MinBase raises the base a contribution is computed on when the salary is
	// lower than it. It is NOT a band: a band can only change a rate applied to
	// a base, and this changes the base. Bulgaria needs both, and they are not
	// the same number — the minimum wage is one figure, while the minimum
	// insurable income (МОД) is a range that depends on occupation.
	MinBase *float64
	Bands   Bands
}

// LegislationPeriod is the payroll law in force from a month: everything a
// government changes in one announcement, in one entry.
//
// Nested this way rather than one dated list per figure because that is how it
// arrives — a January package moves the minimum wage, the contribution
// schedules and sometimes the tax bands together, and splitting one
// announcement across four schedules is four chances to transcribe the date
// differently.
//
// Every figure is optional and carries forward: an entry states what changed,
// not what stayed. Repeating unchanged numbers in every entry is how one of
// them eventually gets repeated wrong. Pointers throughout, so an explicitly
// zeroed rate stays distinguishable from one that was never mentioned.
type LegislationPeriod struct {
	From        yearMonth
	MinimumWage *float64
	Employer    *PartySchedule
	Employee    *PartySchedule
	IncomeTax   Bands
}

// Legislation is the periods in date order.
type Legislation []LegislationPeriod

// PartyRules is one party's resolved schedule for a payroll month.
type PartyRules struct {
	MinBase float64 // 0 = none
	Bands   Bands
}

// Rules are the figures resolved for one payroll month. It holds slices and so
// is not comparable with ==; compare with reflect.DeepEqual.
type Rules struct {
	MinimumEUR float64 // statutory floor on gross salary; 0 = none
	Employer   PartyRules
	Employee   PartyRules
	IncomeTax  Bands
}

// nothingInForce says no entry has stated any figure for this month — before
// the earliest entry, or with no legislation configured at all. The month is
// then charged nothing, which is worth saying out loud on the page: a salary
// with no deductions is indistinguishable from a working one at a glance.
func (r Rules) nothingInForce() bool {
	return r.MinimumEUR == 0 && len(r.IncomeTax) == 0 &&
		r.Employer.MinBase == 0 && len(r.Employer.Bands) == 0 &&
		r.Employee.MinBase == 0 && len(r.Employee.Bands) == 0
}

// BandEntry is one band as config.json writes it.
type BandEntry struct {
	From float64  `json:"from"`
	Rate *float64 `json:"rate"`
}

// PartyEntry is one party's block under "contributions".
type PartyEntry struct {
	MinBase *float64    `json:"minBase"`
	Bands   []BandEntry `json:"bands"`
}

// ContributionsEntry is the "contributions" block: one schedule per party,
// because employer and employee genuinely differ. UK employer NI starts at
// £5,000 a year and employee NI at £12,570; a single shared ceiling has nowhere
// to put two numbers.
type ContributionsEntry struct {
	Employer *PartyEntry `json:"employer"`
	Employee *PartyEntry `json:"employee"`
}

// TaxEntry is the "incomeTax" block. No minBase: a tax base is what it is.
type TaxEntry struct {
	Bands []BandEntry `json:"bands"`
}

// LegislationEntry is one config.json entry. It lives here rather than in the
// config package so the parsing and the meaning stay together.
type LegislationEntry struct {
	From          string              `json:"from"`
	MinimumWage   *float64            `json:"minimumWage"`
	Contributions *ContributionsEntry `json:"contributions"`
	IncomeTax     *TaxEntry           `json:"incomeTax"`

	// The v0.15.0 keys, read for exactly one reason: to reject a file that
	// still carries them and say what to write instead. Dropping them silently
	// would leave a half-migrated file reporting plausible wrong numbers, which
	// is the failure worth being loud about.
	SocialMaxInsurableMonthly *float64 `json:"socialMaxInsurableMonthly"`
	SocialEmployerRate        *float64 `json:"socialEmployerRate"`
	SocialEmployeeRate        *float64 `json:"socialEmployeeRate"`
	IncomeTaxRate             *float64 `json:"incomeTaxRate"`
}

// ParseLegislation turns config.json's entries into periods in date order.
// "2026-07" and "2026-07-01" are both accepted: the day is noise, since these
// figures apply to whole payroll months.
//
// Fail-fast throughout. These are legal obligations, and a config that parses
// into the wrong shape reports a salary nobody is owed.
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
		if e.IncomeTax != nil {
			if p.IncomeTax, err = parseBands(i, ym, "incomeTax", e.IncomeTax.Bands); err != nil {
				return nil, err
			}
			if p.IncomeTax == nil {
				return nil, fmt.Errorf("legislation[%d] (%s): incomeTax names no bands, so it states nothing", i, ym)
			}
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

// rejectRetiredKeys turns a v0.15.0 file into an error that says what to write,
// rather than into a silent half-migration. The message carries the entry's own
// figures so the replacement can be pasted rather than worked out.
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

// formatRate renders a rate for a migration message without the two-decimal
// truncation formatNum applies — 0.1892 has to come back out as 0.1892.
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

// parseBands validates one schedule. An absent list is nil (carry the previous
// one forward); a present list must be usable on its own, since it replaces
// whatever was there.
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
	return p.MinimumWage == nil && p.Employer == nil && p.Employee == nil && p.IncomeTax == nil
}

// String renders a period the way config.json writes it, so what /info shows
// and what the file says can be compared by eye.
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
	if p.IncomeTax != nil {
		parts = append(parts, "tax "+p.IncomeTax.String())
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

// rulesFor resolves the figures for a payroll month by applying every period
// that has taken effect, in order.
//
// Forwards only, and nothing else: a figure applies from the month its entry
// names, stays in force until a later entry changes it, and does not exist
// before that. A month earlier than every entry has nothing in force and is
// charged nothing — there is no fallback to the oldest figures on file and no
// built-in schedule underneath, because either would report a rate that is
// nowhere in config.json for a month nobody legislated.
//
// A month resolving to zero is a real answer and the honest one, but it does
// look like a working payslip, so breakdown flags it — see
// PersonalView.NoLegislation.
func (p PersonalParams) rulesFor(ym yearMonth) Rules { return p.Legislation.rulesAt(ym) }

// rulesAt is rulesFor without needing a PersonalParams around it, so config
// validation can ask the same question the cascade asks.
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
	}
	return r
}

// applyParty carries a party's figures forward one period. MinBase and Bands
// move independently, so an entry that publishes only a new schedule keeps the
// minimum insurable base already in force.
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
