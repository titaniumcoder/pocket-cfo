package actualsdata

import (
	"strings"
	"testing"
)

func TestPartsOfAWholeLine(t *testing.T) {
	parts := PartsOf(Transaction{Id: "a", Amount: 100, Category: strp("groceries")})
	if len(parts) != 1 || parts[0].Category != "groceries" || parts[0].Amount != 100 {
		t.Fatalf("parts = %+v, want the whole line as one part", parts)
	}
}

// TestPartsOfASplitIgnoresTheLineItself: the parts decide, and reading the
// line's own amount as well would count the money twice.
func TestPartsOfASplitIgnoresTheLineItself(t *testing.T) {
	tx := Transaction{Id: "a", Amount: 100, Splits: []Split{
		{Amount: 50, Category: strp("restaurants")},
		{Amount: 30, Category: strp("clothes")},
		{Amount: 20, Ignored: strp("cash in pocket")},
	}}
	parts := PartsOf(tx)
	if len(parts) != 3 {
		t.Fatalf("got %d parts, want 3", len(parts))
	}
	var sum float64
	for _, p := range parts {
		sum += p.Amount
	}
	if sum != tx.Amount {
		t.Errorf("parts sum to %.2f, the line is %.2f", sum, tx.Amount)
	}
	if parts[2].Ignored != "cash in pocket" || parts[2].Category != "" {
		t.Errorf("an ignored part lost its reason: %+v", parts[2])
	}
}

func TestValidateSplits(t *testing.T) {
	known := map[string]bool{"a": true, "b": true}
	base := func(splits []Split) ActualsFile {
		return ActualsFile{
			Month:    "2026-08",
			Coverage: []Coverage{{Account: "A", From: "2026-08-01", To: "2026-08-31", ImportedAt: "2026-09-01"}},
			Transactions: []Transaction{{
				Id: "t1", Date: "2026-08-04", Description: "ATM", Amount: 100, Account: "A", Splits: splits,
			}},
		}
	}
	tests := []struct {
		name   string
		splits []Split
		want   string // substring of the expected complaint; "" means valid
	}{
		{"adds up", []Split{{Amount: 60, Category: strp("a")}, {Amount: 40, Category: strp("b")}}, ""},
		{"an ignored part is allowed", []Split{{Amount: 60, Category: strp("a")}, {Amount: 40, Ignored: strp("cash")}}, ""},
		// Exactly 100 in the units the app counts in, and not in binary
		// floating point — the reason the check rounds to cents first.
		{"thirds that only reconcile in cents", []Split{{Amount: 33.33, Category: strp("a")}, {Amount: 33.33, Category: strp("b")}, {Amount: 33.34, Category: strp("a")}}, ""},
		{"a cent short", []Split{{Amount: 33.33, Category: strp("a")}, {Amount: 33.33, Category: strp("b")}, {Amount: 33.33, Category: strp("a")}}, "add up"},
		{"short by a euro", []Split{{Amount: 60, Category: strp("a")}, {Amount: 39, Category: strp("b")}}, "add up"},
		{"over by a euro", []Split{{Amount: 60, Category: strp("a")}, {Amount: 41, Category: strp("b")}}, "add up"},
		{"a part with neither", []Split{{Amount: 60, Category: strp("a")}, {Amount: 40}}, "neither a category nor an ignored"},
		{"a part with both", []Split{{Amount: 60, Category: strp("a")}, {Amount: 40, Category: strp("b"), Ignored: strp("x")}}, "both a category and an ignored"},
		{"a zero part", []Split{{Amount: 100, Category: strp("a")}, {Amount: 0, Category: strp("b")}}, "amount of 0"},
		{"an unknown category", []Split{{Amount: 60, Category: strp("a")}, {Amount: 40, Category: strp("nope")}}, "not in budget.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateActuals(base(tt.splits), "2026-08", known)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("valid split rejected: %v", err)
			case tt.want == "":
			case err == nil:
				t.Fatalf("accepted, want a complaint about %q", tt.want)
			case !strings.Contains(err.Error(), tt.want):
				t.Fatalf("complaint = %v, want one about %q", err, tt.want)
			}
		})
	}
}

// TestSplitsCannotAlsoCarryALineCategory: the parts decide, so the line
// deciding too would leave two answers and no rule for which wins.
func TestSplitsCannotAlsoCarryALineCategory(t *testing.T) {
	af := ActualsFile{
		Month:    "2026-08",
		Coverage: []Coverage{{Account: "A", From: "2026-08-01", To: "2026-08-31", ImportedAt: "2026-09-01"}},
		Transactions: []Transaction{{
			Id: "t1", Date: "2026-08-04", Description: "ATM", Amount: 100, Account: "A",
			Category: strp("a"),
			Splits:   []Split{{Amount: 60, Category: strp("a")}, {Amount: 40, Category: strp("b")}},
		}},
	}
	err := ValidateActuals(af, "2026-08", map[string]bool{"a": true, "b": true})
	if err == nil || !strings.Contains(err.Error(), "the parts decide") {
		t.Fatalf("err = %v, want a refusal", err)
	}
}
