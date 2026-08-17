package validate

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/titaniumcoder/pocket-cfo/internal/money"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/notes"
	"github.com/titaniumcoder/pocket-cfo/internal/tax"
)

type Doc struct {
	Path string
	Base string
	Inv  *invoice.InvoiceJson
}

var numberPattern = regexp.MustCompile(`^INV-(\d{10})$`)

func Invoice(d Doc, cat *notes.NotesJson) error {
	return errors.Join(
		checkFilename(d),
		checkDates(d),
		checkDiscounts(d),
		invoice.ValidateLocalization(d.Inv),
		checkRegime(d),
		checkNote(d, cat),
	)
}

func checkFilename(d Doc) error {
	if d.Base == "" || d.Base == d.Inv.Number {
		return nil
	}
	return fmt.Errorf("the file is named %s.json but the invoice inside it is %s", d.Base, d.Inv.Number)
}

func checkDates(d Doc) error {
	if d.Inv.DueDate.Time.Before(d.Inv.IssueDate.Time) {
		return fmt.Errorf("due_date %s is before issue_date %s",
			d.Inv.DueDate.Format("2006-01-02"), d.Inv.IssueDate.Format("2006-01-02"))
	}
	return nil
}

func checkDiscounts(d Doc) error {
	if _, err := money.Compute(d.Inv); err != nil {
		return err
	}
	return nil
}

func checkRegime(d Doc) error {
	iss := tax.IssuerOf(d.Inv.Issuer)
	rec := tax.RecipientOf(d.Inv.Recipient)

	resolved, err := tax.Resolve(iss, rec)
	if err != nil {
		return fmt.Errorf("no regime applies to an invoice from %s to %s: %w",
			iss.CountryCode, rec.CountryCode, err)
	}
	if string(resolved.Key) != string(d.Inv.Tax.Regime) {
		return fmt.Errorf("tax.regime is %q but issuer %s to recipient %s (a business%s) resolves to %q — one of the two is wrong",
			d.Inv.Tax.Regime, iss.CountryCode, rec.CountryCode, vatIDNote(rec), resolved.Key)
	}

	if resolved.VATRate == 0 {
		for i, l := range d.Inv.Lines {
			if l.VatRate != 0 {
				return fmt.Errorf("regime %s charges no VAT, but line %d carries %d%%", resolved.Key, i+1, l.VatRate)
			}
		}
	}
	return nil
}

func vatIDNote(rec tax.Recipient) string {
	if rec.VATID == "" {
		return ", no VAT ID"
	}
	return ", VAT ID " + rec.VATID
}

func checkNote(d Doc, cat *notes.NotesJson) error {
	if cat == nil {
		return nil
	}
	regime := string(d.Inv.Tax.Regime)
	entry, ok := cat.Regimes[regime]
	if !ok {
		return fmt.Errorf("catalog has no entry for regime %q, so this invoice's note is backed by nothing", regime)
	}
	if len(entry.MandatoryWording) == 0 {
		return nil
	}

	printed := strings.ToLower(strings.Join(d.Inv.Tax.Note.RenderedTexts(d.Inv.Language), "\n"))
	var problems []error
	for _, w := range entry.MandatoryWording {
		if !strings.Contains(printed, strings.ToLower(w)) {
			problems = append(problems, fmt.Errorf("the note this invoice prints does not contain %q, which %s requires by law", w, regime))
		}
	}
	return errors.Join(problems...)
}

func RecipientReferences(docs []Doc, known map[int]bool) error {
	var problems []error
	for _, d := range docs {
		n := d.Inv.Recipient.Number
		if !known[n] {
			problems = append(problems, fmt.Errorf("%s names recipient %d, which has no file in recipients/ — it will be missing from the recipient ledger", d.Path, n))
		}
	}
	return errors.Join(problems...)
}

func InvoiceSet(docs []Doc) error {
	sorted := make([]Doc, len(docs))
	copy(sorted, docs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Inv.Number < sorted[j].Inv.Number })

	var problems []error
	seen := map[string]string{}
	var ordinals []int
	for _, d := range sorted {
		if first, dup := seen[d.Inv.Number]; dup {
			problems = append(problems, fmt.Errorf("invoice number %s is used by both %s and %s — a number identifies one document",
				d.Inv.Number, first, d.Path))
			continue
		}
		seen[d.Inv.Number] = d.Path

		m := numberPattern.FindStringSubmatch(d.Inv.Number)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		ordinals = append(ordinals, n)
	}

	problems = append(problems, gapProblems(ordinals)...)
	return errors.Join(problems...)
}

func gapProblems(ordinals []int) []error {
	if len(ordinals) == 0 {
		return nil
	}
	sort.Ints(ordinals)

	var problems []error
	if ordinals[0] != 1 {
		problems = append(problems, fmt.Errorf("the sequence starts at %s — invoice numbers are gapless from INV-0000000001", formatNumber(ordinals[0])))
	}
	for i := 1; i < len(ordinals); i++ {
		prev, cur := ordinals[i-1], ordinals[i]
		if cur == prev+1 {
			continue
		}
		if cur == prev+2 {
			problems = append(problems, fmt.Errorf("%s follows %s — %s is missing",
				formatNumber(cur), formatNumber(prev), formatNumber(prev+1)))
			continue
		}
		problems = append(problems, fmt.Errorf("%s follows %s — %s to %s are missing",
			formatNumber(cur), formatNumber(prev), formatNumber(prev+1), formatNumber(cur-1)))
	}
	return problems
}

func formatNumber(n int) string { return fmt.Sprintf("INV-%010d", n) }

func Catalog(c *notes.NotesJson) error {
	var problems []error
	for _, regime := range sortedKeys(c.Regimes) {
		entry := c.Regimes[regime]
		if entry.Text.De == nil || entry.Text.Bg == nil {
			problems = append(problems, fmt.Errorf("regime %q: the note needs both a de and a bg text — every invoice prints the bg", regime))
		}
		for i, w := range entry.MandatoryWording {
			if strings.TrimSpace(w) == "" {
				problems = append(problems, fmt.Errorf("regime %q: mandatory_wording %d is empty, which every note would satisfy", regime, i+1))
			}
		}
	}
	return errors.Join(problems...)
}

func sortedKeys(m notes.NotesJsonRegimes) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
