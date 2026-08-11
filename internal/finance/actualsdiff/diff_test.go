package actualsdiff

import (
	"strings"
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
)

func strp(s string) *string { return &s }

func file(coverage []actualsdata.Coverage, txs ...actualsdata.Transaction) actualsdata.ActualsFile {
	return actualsdata.ActualsFile{Month: "2026-08", Coverage: coverage, Transactions: txs}
}

func cov(account, from, to string) actualsdata.Coverage {
	return actualsdata.Coverage{Account: account, From: from, To: to, ImportedAt: to}
}

func tx(id, date string, amount float64, category string) actualsdata.Transaction {
	return actualsdata.Transaction{Id: id, Date: date, Description: "X", Amount: amount, Account: "A", Category: strp(category)}
}

var wholeAugust = []actualsdata.Coverage{cov("A", "2026-08-01", "2026-08-31")}

func TestDiffCleanCases(t *testing.T) {
	base := file(wholeAugust, tx("t1", "2026-08-01", 900, "rent"))

	tests := []struct {
		name          string
		before, after actualsdata.ActualsFile
	}{
		{
			name:   "identical",
			before: base, after: base,
		},
		{
			name:   "a transaction added",
			before: base,
			after:  file(wholeAugust, tx("t1", "2026-08-01", 900, "rent"), tx("t2", "2026-08-04", 40, "gym")),
		},
		{
			name:   "coverage extended",
			before: file([]actualsdata.Coverage{cov("A", "2026-08-01", "2026-08-09")}, tx("t1", "2026-08-01", 900, "rent")),
			after:  base,
		},
		{
			name:   "two adjacent ranges merged into one",
			before: file([]actualsdata.Coverage{cov("A", "2026-08-01", "2026-08-15"), cov("A", "2026-08-16", "2026-08-31")}, tx("t1", "2026-08-01", 900, "rent")),
			after:  base,
		},
		{
			name:   "a brand new month",
			before: actualsdata.ActualsFile{},
			after:  base,
		},
		{
			name:   "description corrected",
			before: base,
			after: file(wholeAugust, actualsdata.Transaction{
				Id: "t1", Date: "2026-08-01", Description: "MIETE AUGUST (corrected)", Amount: 900, Account: "A", Category: strp("rent"),
			}),
		},
		{
			name:   "a new account added to coverage",
			before: base,
			after:  file([]actualsdata.Coverage{cov("A", "2026-08-01", "2026-08-31"), cov("B", "2026-08-01", "2026-08-31")}, tx("t1", "2026-08-01", 900, "rent")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Diff(tt.before, tt.after); len(got) != 0 {
				t.Errorf("Diff() = %v, want no changes", got)
			}
		})
	}
}

func TestDiffCatchesRemoval(t *testing.T) {
	before := file(wholeAugust, tx("t1", "2026-08-01", 900, "rent"), tx("t2", "2026-08-04", 40, "gym"))
	after := file(wholeAugust, tx("t2", "2026-08-04", 40, "gym"))

	got := Diff(before, after)
	if len(got) != 1 {
		t.Fatalf("Diff() = %v, want exactly one change", got)
	}
	if got[0].Kind != Removed || got[0].ID != "t1" {
		t.Errorf("got %+v, want a removal naming t1", got[0])
	}
}

func TestDiffCatchesEachMutatedField(t *testing.T) {
	base := tx("t1", "2026-08-01", 900, "rent")
	tests := []struct {
		name   string
		change func(actualsdata.Transaction) actualsdata.Transaction
		want   string
	}{
		{"date", func(x actualsdata.Transaction) actualsdata.Transaction { x.Date = "2026-08-02"; return x }, "date"},
		{"amount", func(x actualsdata.Transaction) actualsdata.Transaction { x.Amount = 950; return x }, "amount"},
		{"account", func(x actualsdata.Transaction) actualsdata.Transaction { x.Account = "B"; return x }, "account"},
		{"category", func(x actualsdata.Transaction) actualsdata.Transaction { x.Category = strp("other"); return x }, "category"},
		{"ignored", func(x actualsdata.Transaction) actualsdata.Transaction {
			x.Category = nil
			x.Ignored = strp("salary")
			return x
		}, "ignored"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Diff(file(wholeAugust, base), file(wholeAugust, tt.change(base)))
			if len(got) == 0 {
				t.Fatalf("a changed %s went unreported", tt.name)
			}
			found := false
			for _, c := range got {
				if c.Kind == Mutated && strings.Contains(c.Detail, tt.want) {
					found = true
				}
			}
			if !found {
				t.Errorf("Diff() = %v, want a mutation mentioning %q", got, tt.want)
			}
		})
	}
}

func TestDiffCatchesCoverageRegression(t *testing.T) {
	before := file(wholeAugust, tx("t1", "2026-08-01", 900, "rent"))
	after := file([]actualsdata.Coverage{cov("A", "2026-08-15", "2026-08-31")}, tx("t1", "2026-08-01", 900, "rent"))

	got := Diff(before, after)
	if len(got) != 1 {
		t.Fatalf("Diff() = %v, want exactly one change", got)
	}
	if got[0].Kind != CoverageShrank || got[0].ID != "A" {
		t.Errorf("got %+v, want a coverage regression naming account A", got[0])
	}
	if !strings.Contains(got[0].Detail, "14 day(s)") {
		t.Errorf("detail = %q, want it to count the lost days", got[0].Detail)
	}
}

// TestDiffCatchesADroppedAccount covers the case where a whole account stops
// being imported: every one of its days is lost at once.
func TestDiffCatchesADroppedAccount(t *testing.T) {
	before := file([]actualsdata.Coverage{cov("A", "2026-08-01", "2026-08-31"), cov("B", "2026-08-01", "2026-08-31")})
	after := file(wholeAugust)

	got := Diff(before, after)
	if len(got) != 1 || got[0].ID != "B" {
		t.Fatalf("Diff() = %v, want one regression naming account B", got)
	}
}

// TestDiffReportsEverything pins that a submission which both drops a
// transaction and shrinks coverage reports both, rather than the first.
func TestDiffReportsEverything(t *testing.T) {
	before := file(wholeAugust, tx("t1", "2026-08-01", 900, "rent"), tx("t2", "2026-08-04", 40, "gym"))
	after := file([]actualsdata.Coverage{cov("A", "2026-08-01", "2026-08-09")}, tx("t2", "2026-08-04", 999, "gym"))

	got := Diff(before, after)
	kinds := map[string]bool{}
	for _, c := range got {
		kinds[c.Kind] = true
	}
	for _, want := range []string{Removed, Mutated, CoverageShrank} {
		if !kinds[want] {
			t.Errorf("Diff() = %v, want it to include a %s", got, want)
		}
	}
}
