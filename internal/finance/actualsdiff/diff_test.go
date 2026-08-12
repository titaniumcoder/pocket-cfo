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

// TestDiffCatchesADroppedAccount: a whole account stops being imported.
func TestDiffCatchesADroppedAccount(t *testing.T) {
	before := file([]actualsdata.Coverage{cov("A", "2026-08-01", "2026-08-31"), cov("B", "2026-08-01", "2026-08-31")})
	after := file(wholeAugust)

	got := Diff(before, after)
	if len(got) != 1 || got[0].ID != "B" {
		t.Fatalf("Diff() = %v, want one regression naming account B", got)
	}
}

// TestDiffReportsEverything: a submission breaking several rules reports all
// of them.
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

func split(id, date string, amount float64, parts ...actualsdata.Split) actualsdata.Transaction {
	return actualsdata.Transaction{Id: id, Date: date, Description: "X", Amount: amount, Account: "A", Splits: parts}
}

// TestDiffSeesSplitChanges: re-uploading a statement can re-split a line
// without touching its id, date, amount, account or the fields the diff
// already watched — so without this, money moves between categories and the
// anti-vanish check reports a clean run.
func TestDiffSeesSplitChanges(t *testing.T) {
	base := file(wholeAugust, split("w1", "2026-08-14", 100,
		actualsdata.Split{Amount: 60, Category: strp("restaurants")},
		actualsdata.Split{Amount: 40, Category: strp("clothes")},
	))

	tests := []struct {
		name  string
		after actualsdata.ActualsFile
		want  bool
	}{
		{"identical", file(wholeAugust, split("w1", "2026-08-14", 100,
			actualsdata.Split{Amount: 60, Category: strp("restaurants")},
			actualsdata.Split{Amount: 40, Category: strp("clothes")})), false},
		{"a part moves to another category", file(wholeAugust, split("w1", "2026-08-14", 100,
			actualsdata.Split{Amount: 60, Category: strp("restaurants")},
			actualsdata.Split{Amount: 40, Category: strp("groceries")})), true},
		{"the parts are re-weighted", file(wholeAugust, split("w1", "2026-08-14", 100,
			actualsdata.Split{Amount: 80, Category: strp("restaurants")},
			actualsdata.Split{Amount: 20, Category: strp("clothes")})), true},
		{"a part becomes ignored", file(wholeAugust, split("w1", "2026-08-14", 100,
			actualsdata.Split{Amount: 60, Category: strp("restaurants")},
			actualsdata.Split{Amount: 40, Ignored: strp("cash")})), true},
		{"the split collapses to one category", file(wholeAugust,
			tx("w1", "2026-08-14", 100, "restaurants")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changes := Diff(base, tt.after)
			if got := len(changes) > 0; got != tt.want {
				t.Fatalf("changes = %v, want any=%v", changes, tt.want)
			}
			if !tt.want {
				return
			}
			var mutated bool
			for _, c := range changes {
				if strings.Contains(c.Detail, "splits") || strings.Contains(c.Detail, "category") {
					mutated = true
				}
			}
			if !mutated {
				t.Errorf("changes = %v, want one naming the split", changes)
			}
		})
	}
}
