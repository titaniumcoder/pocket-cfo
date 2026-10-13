package actualsdata

type Part struct {
	Category  string
	Ignored   string
	Untracked string
	Amount    float64
}

func PartsOf(tx Transaction) []Part {
	if len(tx.Splits) > 0 {
		return partsOfSplits(tx.Splits)
	}
	return []Part{wholeLineAsPart(tx)}
}

func partsOfSplits(splits []Split) []Part {
	parts := make([]Part, 0, len(splits))
	for _, s := range splits {
		parts = append(parts, Part{
			Category:  deref(s.Category),
			Ignored:   deref(s.Ignored),
			Untracked: deref(s.Untracked),
			Amount:    s.Amount,
		})
	}
	return parts
}

func wholeLineAsPart(tx Transaction) Part {
	return Part{
		Category:  deref(tx.Category),
		Ignored:   deref(tx.Ignored),
		Untracked: deref(tx.Untracked),
		Amount:    tx.Amount,
	}
}

func SplitSum(tx Transaction) float64 {
	var sum float64
	for _, s := range tx.Splits {
		sum += s.Amount
	}
	return sum
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
