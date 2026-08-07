// Package render turns an invoice into HTML (this file) and a PDF (see
// api2pdf.go). See ARCHITECTURE.md §6.
package render

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"strconv"
	"strings"

	"github.com/atombender/go-jsonschema/pkg/types"

	"github.com/titaniumcoder/pocket-cfo/internal/money"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

// templatePath defaults to this repo's own invoice template, overridable via
// TEMPLATES_DIR (shared with cmd/pocketcfo, which reads its own web templates
// from the same directory) — e.g. for a deployment with its own branding.
var templatePath = getenv("TEMPLATES_DIR", "templates") + "/invoice.html.tmpl"

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// View is everything the invoice template needs.
type View struct {
	Invoice   *invoice.InvoiceJson
	Totals    money.Totals
	Labels    Labels
	AmountDue int64
	IsPaid    bool
	IsDraft   bool
	HasVAT    bool
	LogoSVG   template.HTML
}

// HTML renders inv to a self-contained HTML document. templatePath is read
// from disk relative to the repo root, like templates/ elsewhere — see
// AGENTS.md.
//
// showPaid decides whether the paid badge/stamp and zeroed amount-due are
// shown — deliberately independent of inv.Paid. Per ARCHITECTURE.md §6, the
// original PDF is written once and never touched, so it must always render
// as if unpaid regardless of the invoice's actual payment state; only the
// separate -paid.pdf artifact (rendered with showPaid=true) reflects it.
func HTML(inv *invoice.InvoiceJson, totals money.Totals, showPaid bool) ([]byte, error) {
	if err := validateLocalization(inv); err != nil {
		return nil, fmt.Errorf("localization: %w", err)
	}

	tmpl, err := template.New("invoice.html.tmpl").Funcs(funcMap).ParseFiles(templatePath)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}

	hasVAT := false
	for _, g := range totals.VATGroups {
		if g.Rate != 0 {
			hasVAT = true
			break
		}
	}

	amountDue := totals.GrandTotal
	if showPaid {
		amountDue = 0
	}

	var logoSVG template.HTML
	if inv.Issuer.Logo != nil && *inv.Issuer.Logo != "" {
		svg, err := loadLogoSVG(*inv.Issuer.Logo)
		if err != nil {
			return nil, fmt.Errorf("load logo: %w", err)
		}
		logoSVG = svg
	}

	view := View{
		Invoice:   inv,
		Totals:    totals,
		Labels:    CombinedLabels(inv.Language),
		AmountDue: amountDue,
		IsPaid:    showPaid,
		IsDraft:   inv.Status == invoice.InvoiceJsonStatusDraft,
		HasVAT:    hasVAT,
		LogoSVG:   logoSVG,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, view); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	return buf.Bytes(), nil
}

var funcMap = template.FuncMap{
	"money":     FormatMoney,
	"qty":       formatScaledHundredths,
	"percent":   formatPercentPtr,
	"date":      FormatDate,
	"dateptr":   formatDatePtr,
	"str":       derefString,
	"country":   CountryName,
	"bilingual": bilingual,
}

// thousandsSeparator is a non-breaking space, matching Bulgarian/European
// grouping convention ("1 000,25") — a regular space would let a number
// wrap mid-figure across a line.
const thousandsSeparator = ' '

// FormatMoney renders minor units as "1 234,56 €" — nbsp-grouped thousands,
// decimal-comma, euro suffix. Picked and frozen per ARCHITECTURE.md §6/§11.
func FormatMoney(minor int64) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	major := minor / 100
	cents := minor % 100

	digits := strconv.FormatInt(major, 10)
	var grouped strings.Builder
	for i, c := range digits {
		if i != 0 && (len(digits)-i)%3 == 0 {
			grouped.WriteRune(thousandsSeparator)
		}
		grouped.WriteRune(c)
	}

	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%s,%02d €", sign, grouped.String(), cents)
}

// loadLogoSVG reads an SVG file from disk (relative to the repo root, like
// templates/ elsewhere — see AGENTS.md) and strips the leading XML
// declaration, which isn't valid as a literal token inside an HTML
// document. The rest of the markup, including any Inkscape/sodipodi
// metadata attributes, is left untouched — browsers ignore what they don't
// recognize.
func loadLogoSVG(path string) (template.HTML, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimLeft(string(b), " \t\r\n")
	if strings.HasPrefix(s, "<?xml") {
		if i := strings.Index(s, "?>"); i != -1 {
			s = strings.TrimLeft(s[i+2:], " \t\r\n")
		}
	}
	return template.HTML(s), nil
}

// formatScaledHundredths renders a value stored scaled by 100 (quantity,
// percent — see ARCHITECTURE.md §3.4/§3.5) as a trimmed decimal: 100 -> "1",
// 13600 -> "136", 350 -> "3.5". A nil pointer is the "absent" case and
// renders as "1".
func formatScaledHundredths(scaled *int) string {
	v := 100
	if scaled != nil {
		v = *scaled
	}
	whole := v / 100
	frac := v % 100
	if frac == 0 {
		return strconv.Itoa(whole)
	}
	s := fmt.Sprintf("%d.%02d", whole, frac)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

func formatPercentPtr(scaled *int) string {
	if scaled == nil {
		return ""
	}
	return formatScaledHundredths(scaled)
}

// FormatDate renders a date as "02.01.2006" (day.month.year), the format
// used throughout the invoice template and the web dashboard.
func FormatDate(d types.SerializableDate) string {
	return d.Format("02.01.2006")
}

func formatDatePtr(d *types.SerializableDate) string {
	if d == nil {
		return ""
	}
	return d.Format("02.01.2006")
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
