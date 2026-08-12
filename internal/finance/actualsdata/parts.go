package actualsdata

// Part is one attribution of money from a statement line: a category it was
// spent on, a reason it is not an expense, or a note saying it has not been
// decided yet. A line that paid for one thing has a single part; a split has
// one per entry.
//
// Untracked is deliberately a third field rather than a reserved category id.
// Every figure in the app skips a part with no Category, so untracked money
// stays out of ByCategory, the dashboard ledger and the reconciliation totals
// without a single one of them opting out — and a figure added later inherits
// that for free. A category would have had to be excluded in six places, and
// the one that got missed would have been wrong in silence.
type Part struct {
	Category  string
	Ignored   string
	Untracked string
	Amount    float64
}

// PartsOf is how every figure in the app reads a transaction. Nothing should
// reach for Category directly: a split has none, so code that does silently
// drops the whole line from its total — the failure mode that makes a split
// worse than not supporting splits at all.
func PartsOf(tx Transaction) []Part {
	if len(tx.Splits) > 0 {
		parts := make([]Part, 0, len(tx.Splits))
		for _, s := range tx.Splits {
			parts = append(parts, Part{
				Category:  deref(s.Category),
				Ignored:   deref(s.Ignored),
				Untracked: deref(s.Untracked),
				Amount:    s.Amount,
			})
		}
		return parts
	}
	return []Part{{
		Category:  deref(tx.Category),
		Ignored:   deref(tx.Ignored),
		Untracked: deref(tx.Untracked),
		Amount:    tx.Amount,
	}}
}

// SplitSum adds up a split's parts, for the check that they reconcile to the
// line they came from.
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
