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

var templatePath = getenv("TEMPLATES_DIR", "templates") + "/invoice.html.tmpl"

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type View struct {
	Invoice   *invoice.InvoiceJson
	Totals    money.Totals
	Labels    Labels
	AmountDue int64
	PaidOn    *types.SerializableDate
	IsPaid    bool
	IsDraft   bool
	HasVAT    bool
	LogoSVG   template.HTML
}

func HTML(inv *invoice.InvoiceJson, totals money.Totals, paidOn *types.SerializableDate) ([]byte, error) {
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
	if paidOn != nil {
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
		PaidOn:    paidOn,
		IsPaid:    paidOn != nil,
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

const thousandsSeparator = ' '

func FormatMoney(minor int64) string {
	return FormatAmount(minor) + " €"
}

func FormatAmount(minor int64) string {
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
	return fmt.Sprintf("%s%s,%02d", sign, grouped.String(), cents)
}

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
