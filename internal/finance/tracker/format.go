package tracker

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

func round(v float64) int { return int(math.Round(v)) }

// formatNum renders a number with up to two decimals, trimming trailing zeros
// (e.g. 75 -> "75", 75.5 -> "75.5"). Used for the rate.
func formatNum(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// formatHM renders hours as H:MM (e.g. 18.25 -> "18:15", 9.75 -> "9:45").
func formatHM(h float64) string {
	totalMin := int(math.Round(h * 60))
	return fmt.Sprintf("%d:%02d", totalMin/60, totalMin%60)
}

func formatCompactHours(h float64) string {
	totalMin := int(math.Round(h * 60))
	if totalMin%60 == 0 {
		return strconv.Itoa(totalMin / 60)
	}
	return fmt.Sprintf("%d:%02d", totalMin/60, totalMin%60)
}

// formatEuro renders cents as whole euros (rounded) with thousands separators and
// no decimals (e.g. 136875 -> "1,369").
// formatDay renders a date from the data files the way the rest of the app
// writes dates — day first, as the invoices do. The files store ISO because
// that is what sorts and validates; the screen is a different question.
//
// An unparseable value is shown as written rather than swallowed: a date the
// app cannot read is exactly the one worth seeing.
func formatDay(iso string) string {
	d, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return d.Format("02.01.2006")
}

func formatEuro(cents int) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	euros := (cents + 50) / 100 // round to the nearest euro
	return sign + groupThousands(euros)
}

// groupThousands renders a non-negative integer with comma thousands separators
// (e.g. 1234567 -> "1,234,567").
func groupThousands(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		b.WriteByte(',')
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	return b.String()
}

// CurrencySymbol maps an ISO currency code to a display symbol.
func CurrencySymbol(code string) string {
	switch strings.ToUpper(code) {
	case "USD":
		return "$"
	case "GBP":
		return "£"
	case "JPY":
		return "¥"
	case "EUR", "":
		return "€"
	default:
		return code + " "
	}
}

// truncHours strips the ":MM" minutes portion from a formatted hours string
// (e.g. "18:15" → "18", "160" → "160"). Used to show compact hour-only labels
// on narrow screens where the full "Xh × rate" layout doesn't fit.
func truncHours(hm string) string {
	if i := strings.Index(hm, ":"); i >= 0 {
		return hm[:i]
	}
	return hm
}
