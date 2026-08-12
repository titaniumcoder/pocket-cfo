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

// TestPartsOfCarriesUntracked: every figure in the app reads through PartsOf,
// so a part that dropped its untracked note on the way through would be money
// with no category and no explanation — indistinguishable from a bug.
func TestPartsOfCarriesUntracked(t *testing.T) {
	line := PartsOf(Transaction{Id: "a", Amount: 100, Untracked: strp("ATM, not spent yet")})
	if len(line) != 1 || line[0].Untracked != "ATM, not spent yet" || line[0].Category != "" {
		t.Fatalf("whole line = %+v, want one untracked part with no category", line)
	}

	split := PartsOf(Transaction{Id: "b", Amount: 100, Splits: []Split{
		{Amount: 60, Category: strp("groceries")},
		{Amount: 40, Untracked: strp("still in my wallet")},
	}})
	if len(split) != 2 {
		t.Fatalf("got %d parts, want 2", len(split))
	}
	if split[1].Untracked != "still in my wallet" || split[1].Category != "" || split[1].Ignored != "" {
		t.Errorf("untracked part = %+v, want only the note", split[1])
	}
}

// TestUntrackedIsADecision: untracked is the decision to decide later, so a
// line carrying one is complete. The rule it must not weaken is the one that
// says nothing may be left blank.
func TestUntrackedIsADecision(t *testing.T) {
	base := func(tx Transaction) ActualsFile {
		return ActualsFile{
			Month:        "2026-08",
			Coverage:     []Coverage{{Account: "A", From: "2026-08-01", To: "2026-08-31", ImportedAt: "2026-09-01"}},
			Transactions: []Transaction{tx},
		}
	}
	tx := Transaction{Id: "t1", Date: "2026-08-04", Description: "ATM", Amount: 100, Account: "A"}

	untracked := tx
	untracked.Untracked = strp("cash, not spent yet")
	if err := ValidateActuals(base(untracked), "2026-08", map[string]bool{"a": true}); err != nil {
		t.Fatalf("an untracked line was rejected: %v", err)
	}

	// It is not a way to smuggle a second disposition onto a line.
	both := tx
	both.Untracked, both.Category = strp("cash"), strp("a")
	err := ValidateActuals(base(both), "2026-08", map[string]bool{"a": true})
	if err == nil || !strings.Contains(err.Error(), "more than one of category") {
		t.Fatalf("err = %v, want a refusal of category plus untracked", err)
	}

	// And a blank line is still a blank line.
	blank := tx
	err = ValidateActuals(base(blank), "2026-08", map[string]bool{"a": true})
	if err == nil || !strings.Contains(err.Error(), "no line may be left undecided") {
		t.Fatalf("err = %v, want the undecided-line refusal", err)
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
		{"a part with none", []Split{{Amount: 60, Category: strp("a")}, {Amount: 40}}, "none of a category"},
		{"a part with two", []Split{{Amount: 60, Category: strp("a")}, {Amount: 40, Category: strp("b"), Ignored: strp("x")}}, "more than one of category"},
		// The case a split exists for: some of the cash is placeable and the
		// rest is not, recorded as exactly that rather than guessed at.
		{"an untracked part is allowed", []Split{{Amount: 60, Category: strp("a")}, {Amount: 40, Untracked: strp("still in my wallet")}}, ""},
		{"a part untracked and categorised", []Split{{Amount: 60, Category: strp("a")}, {Amount: 40, Category: strp("b"), Untracked: strp("x")}}, "more than one of category"},
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
