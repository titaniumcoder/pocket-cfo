package tracker

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

func round(v float64) int { return int(math.Round(v)) }

func formatNum(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

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
	return sign + groupThousands(roundedToWholeEuros(cents))
}

func roundedToWholeEuros(cents int) int { return (cents + 50) / 100 }

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

func truncHours(hm string) string {
	if i := strings.Index(hm, ":"); i >= 0 {
		return hm[:i]
	}
	return hm
}
