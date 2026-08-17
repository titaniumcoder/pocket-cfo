package actualsdata

type Part struct {
	Category  string
	Ignored   string
	Untracked string
	Movement  Movement
	Amount    float64
}

// Crossed says the money reached the owner, so it settles the director's loan.
// It is NOT the same set as MovedCompanyCash, and the two are easy to confuse:
// a salary transfer settles the loan but is already in the company's figures
// as gross salary, and a tax payment leaves the company without ever reaching
// the owner.
func (p Part) Crossed() bool {
	switch p.Movement {
	case MovementSalaryTransfer, MovementOwnerDraw, MovementDividendPayout, MovementOwnerContribution:
		return true
	}
	return false
}

// MovedCompanyCash says the money left the company's bank, or arrived in it.
// A salary transfer is excluded on purpose: the cascade already subtracts the
// gross salary, which covers the net paid to the owner and what is remitted to
// the state for them, so counting the transfer as well would pay the salary
// twice.
func (p Part) MovedCompanyCash() bool {
	switch p.Movement {
	case MovementOwnerDraw, MovementDividendPayout, MovementOwnerContribution,
		MovementCorporateTax, MovementDividendTax:
		return true
	}
	return false
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
			Movement:  derefMovement(s.Movement),
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
		Movement:  derefMovement(tx.Movement),
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

func derefMovement(m *Movement) Movement {
	if m == nil {
		return ""
	}
	return *m
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
