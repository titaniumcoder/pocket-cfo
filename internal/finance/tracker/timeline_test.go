package tracker

import (
	"strings"
	"testing"
	"time"
)

func timelineParams(t *testing.T) PersonalParams {
	t.Helper()
	legislation, err := ParseLegislation([]LegislationEntry{
		{From: "2026-01",
			Contributions:    &ContributionsEntry{Employer: &PartyEntry{Bands: []BandEntry{{From: 0, Rate: f64(0.1892)}, {From: 2112, Rate: f64(0)}}}},
			IncomeTax:        &TaxEntry{Bands: []BandEntry{{From: 0, Rate: f64(0.10)}}},
			CompanyProfitTax: &TaxEntry{Bands: []BandEntry{{From: 0, Rate: f64(0.10)}}},
			DividendTax:      &TaxEntry{Bands: []BandEntry{{From: 0, Rate: f64(0.05)}}}},
		{From: "2026-07", MinimumWage: f64(1077)},
	})
	if err != nil {
		t.Fatal(err)
	}
	salary, err := ParseSalaryPlan([]SalaryEntry{{From: "2026-09", To: "2026-10", Mode: "fixed", Amount: f64(2500)}})
	if err != nil {
		t.Fatal(err)
	}
	target, err := ParseTargetPlan([]TargetEntry{{From: "2026-11", Amount: f64(15000)}})
	if err != nil {
		t.Fatal(err)
	}
	return PersonalParams{Legislation: legislation, Salary: salary, Target: target}
}

func TestRulesTimelineHasAnEntryPerChangeMonth(t *testing.T) {
	today := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	entries := RulesTimeline(timelineParams(t), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), today)

	var labels []string
	for _, e := range entries {
		labels = append(labels, e.Label+": "+strings.Join(e.Changes, ", "))
	}
	want := []string{
		"January 2026: legislation",
		"April 2026: start month",
		"July 2026: legislation",
		"September 2026: salary",
		"November 2026: salary, target balance",
	}
	if strings.Join(labels, " | ") != strings.Join(want, " | ") {
		t.Errorf("entries = %v\nwant      %v", labels, want)
	}
	if entries[0].Anchor != "rules-2026-01" {
		t.Errorf("Anchor = %q", entries[0].Anchor)
	}
	for i, e := range entries {
		if e.Current != (e.Label == "September 2026") {
			t.Errorf("entry %d (%s) Current = %v", i, e.Label, e.Current)
		}
	}
}

func TestRulesTimelineMarksWhatChangedAndWhatCarriedForward(t *testing.T) {
	entries := RulesTimeline(timelineParams(t), time.Time{}, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))
	rows := func(label string) map[string]RuleRow {
		for _, e := range entries {
			if e.Label == label {
				out := map[string]RuleRow{}
				for _, r := range e.Rules {
					out[r.Name] = r
				}
				return out
			}
		}
		t.Fatalf("no entry for %s", label)
		return nil
	}

	jan := rows("January 2026")
	if r := jan["Dividend tax"]; r.Value != "5%" || !r.Changed || r.Since != "" {
		t.Errorf("January dividend tax = %+v", r)
	}
	if r := jan["Company profit tax"]; r.Value != "10%" || !r.Changed {
		t.Errorf("January company profit tax = %+v", r)
	}
	if r := jan["Minimum wage"]; r.Value != "none" || r.Changed || r.Since != "" {
		t.Errorf("January minimum wage = %+v, want none and unstated", r)
	}
	if r := jan["Employer contributions"]; r.Value != "18.92% to 2112, then 0%" || !r.Changed {
		t.Errorf("January employer = %+v", r)
	}
	if r := jan["Employee contributions"]; r.Value != "none" {
		t.Errorf("January employee = %+v", r)
	}

	jul := rows("July 2026")
	if r := jul["Minimum wage"]; r.Value != "1,077" || !r.Changed {
		t.Errorf("July minimum wage = %+v", r)
	}
	if r := jul["Dividend tax"]; r.Value != "5%" || r.Changed || r.Since != "January 2026" {
		t.Errorf("July dividend tax = %+v, want carried forward since January 2026", r)
	}
}

func TestRulesTimelineSaysWhatSalaryAndTargetApply(t *testing.T) {
	entries := RulesTimeline(timelineParams(t), time.Time{}, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))
	row := func(label, name string) RuleRow {
		for _, e := range entries {
			if e.Label != label {
				continue
			}
			for _, r := range e.Rules {
				if r.Name == name {
					return r
				}
			}
		}
		t.Fatalf("no %s row for %s", name, label)
		return RuleRow{}
	}
	if r := row("July 2026", "Salary"); r.Value != "full (nothing configured)" || r.Changed || r.Since != "" {
		t.Errorf("July salary = %+v, want the never-configured default, unmarked", r)
	}
	if r := row("September 2026", "Salary"); r.Value != "fixed 2,500 — September 2026 to October 2026" || !r.Changed {
		t.Errorf("September salary = %+v, want the fixed span, changed", r)
	}
	if r := row("November 2026", "Salary"); r.Value != "full (nothing configured)" || !r.Changed {
		t.Errorf("November salary = %+v, want the fallback after the closed span, changed", r)
	}
	if r := row("September 2026", "Target balance"); r.Value != "none" || r.Changed || r.Since != "" {
		t.Errorf("September target = %+v, want none, unmarked", r)
	}
	if r := row("November 2026", "Target balance"); r.Value != "15,000 — from November 2026" || !r.Changed {
		t.Errorf("November target = %+v, want the new target, changed", r)
	}
	if r := row("July 2026", "Dividend tax"); r.Changed || r.Since != "January 2026" {
		t.Errorf("July dividend tax = %+v, want carried forward", r)
	}
}

func TestRulesTimelineCarriesSalaryAndTargetForwardToo(t *testing.T) {
	p := timelineParams(t)
	salary, err := ParseSalaryPlan([]SalaryEntry{{From: "2026-03", Mode: "minimum"}})
	if err != nil {
		t.Fatal(err)
	}
	target, err := ParseTargetPlan([]TargetEntry{{From: "2026-03", Amount: f64(9000)}})
	if err != nil {
		t.Fatal(err)
	}
	p.Salary, p.Target = salary, target
	entries := RulesTimeline(p, time.Time{}, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))
	for _, e := range entries {
		if e.Label != "July 2026" {
			continue
		}
		for _, r := range e.Rules {
			if (r.Name == "Salary" || r.Name == "Target balance") && (r.Changed || r.Since != "March 2026") {
				t.Errorf("July %s = %+v, want carried forward since March 2026", r.Name, r)
			}
		}
	}
}

func TestRulesTimelineCarriesTheIdleTargetNotes(t *testing.T) {
	p := timelineParams(t)
	target, err := ParseTargetPlan([]TargetEntry{{From: "2026-09", Amount: f64(15000)}})
	if err != nil {
		t.Fatal(err)
	}
	p.Target = target
	entries := RulesTimeline(p, time.Time{}, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))
	var sep RuleChange
	for _, e := range entries {
		if e.Label == "September 2026" {
			sep = e
		}
	}
	if len(sep.Notes) != 2 || !strings.Contains(sep.Notes[0], "2026-09 (salary is fixed then)") || !strings.Contains(sep.Notes[1], "2026-10") {
		t.Errorf("September notes = %v, want the two idle months", sep.Notes)
	}
}

func TestRulesTimelineIsEmptyWithoutDatedRules(t *testing.T) {
	if got := RulesTimeline(PersonalParams{}, time.Time{}, time.Now()); len(got) != 0 {
		t.Errorf("entries = %+v, want none", got)
	}
}

func TestRulesTimelineBeforeTheFirstEntryHasNoCurrent(t *testing.T) {
	entries := RulesTimeline(timelineParams(t), time.Time{}, time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))
	for _, e := range entries {
		if e.Current {
			t.Errorf("%s is current before any rule applies", e.Label)
		}
	}
}
