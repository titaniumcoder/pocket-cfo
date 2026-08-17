package api

import (
	"context"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

type DividendPlanned = tracker.DividendReport

type LegislationPeriod struct {
	From    string `json:"from"`
	InForce string `json:"in_force"`
}

type FinanceConfig struct {
	Legislation   []LegislationPeriod `json:"legislation"`
	Salary        []string            `json:"salary"`
	TargetBalance []string            `json:"target_balance"`
	ReadOnly      string              `json:"read_only"`
}

const configIsDeployment = "Deployment configuration. It is changed by editing config.json and redeploying, never through this API — there is no tool that writes it, and that is deliberate: these are the rates a past payslip has to stay reproducible against."

// FinanceConfig reports the dated rules every figure on the finance pages is
// computed from, so an agent asked to explain one can read the rule rather
// than guess at it.
func (s *Service) FinanceConfig(context.Context) (*FinanceConfig, error) {
	out := &FinanceConfig{ReadOnly: configIsDeployment}
	for _, p := range s.Personal.Legislation {
		out.Legislation = append(out.Legislation, LegislationPeriod{From: p.From.ConfigForm(), InForce: p.String()})
	}
	for _, p := range s.Personal.Salary {
		out.Salary = append(out.Salary, p.String())
	}
	for _, p := range s.Personal.Target {
		out.TargetBalance = append(out.TargetBalance, p.String())
	}
	return out, nil
}

type DirectorLoan struct {
	Month             string   `json:"month"`
	Known             bool     `json:"known"`
	Unknown           string   `json:"unknown,omitempty"`
	OpeningCents      int      `json:"opening_cents"`
	NetIncomeCents    int      `json:"net_income_cents"`
	MovementCents     int      `json:"movement_cents"`
	ClosingCents      int      `json:"closing_cents"`
	SettledBy         []Settle `json:"settled_by,omitempty"`
	Notes             []string `json:"notes,omitempty"`
	PositiveMeans     string   `json:"positive_means"`
	ReachesNoOtherSum string   `json:"reaches_no_other_figure"`
}

type Settle struct {
	Movement string `json:"movement"`
	Cents    int    `json:"cents"`
}

// DirectorLoanFor answers the same question the dashboard's own block does,
// from the same computation, so the two cannot disagree.
func (s *Service) DirectorLoanFor(ctx context.Context, month string) (*DirectorLoan, error) {
	year, m, err := ParseMonth(month)
	if err != nil {
		return nil, err
	}
	if s.Figures == nil {
		return nil, errorf(CodeInternal, "the finance figures are not wired up")
	}
	f, err := s.Figures(ctx, year, m)
	if err != nil {
		return nil, errorf(CodeInternal, "computing %s: %v", month, err)
	}

	out := &DirectorLoan{
		Month:             month,
		Known:             f.ShowDirectorLoan && f.DirectorLoanUnknown == "",
		Unknown:           f.DirectorLoanUnknown,
		OpeningCents:      f.LoanOpeningCents,
		NetIncomeCents:    f.LoanNetIncomeCents,
		MovementCents:     f.LoanMovementCents,
		ClosingCents:      f.LoanClosingCents,
		Notes:             f.DirectorLoanNotes,
		PositiveMeans:     "the company owes the owner; negative means the owner owes the company",
		ReachesNoOtherSum: "read-only: it feeds no other figure, and the private balance still assumes net income lands in the account",
	}
	for _, mv := range s.movementsIn(ctx, year, m) {
		out.SettledBy = append(out.SettledBy, mv)
	}
	return out, nil
}

func (s *Service) movementsIn(ctx context.Context, year int, month time.Month) []Settle {
	if s.Actuals == nil {
		return nil
	}
	av, err := s.Actuals.ForMonth(ctx, year, month)
	if err != nil || !av.Present {
		return nil
	}
	out := make([]Settle, 0, len(av.ByMovementRow))
	for _, row := range av.ByMovementRow {
		out = append(out, Settle{Movement: string(row.Movement), Cents: row.Cents})
	}
	return out
}
