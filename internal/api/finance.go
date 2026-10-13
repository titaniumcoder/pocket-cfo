package api

import (
	"context"
)

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
