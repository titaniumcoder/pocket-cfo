package actualsdata

import (
	"strings"
	"testing"
)

func strp(s string) *string { return &s }

// validFile is a minimal document every test mutates one aspect of.
func validFile() ActualsFile {
	return ActualsFile{
		Month: "2026-08",
		Coverage: []Coverage{
			{Account: "A", From: "2026-08-01", To: "2026-08-31", ImportedAt: "2026-09-01"},
		},
		Transactions: []Transaction{
			{Id: "t1", Date: "2026-08-03", Description: "LIDL", Amount: 42.18, Account: "A", Category: strp("food.groceries")},
			{Id: "t2", Date: "2026-08-02", Description: "SALARY", Amount: -2400, Account: "A", Ignored: strp("salary")},
		},
	}
}

var known = map[string]bool{"food.groceries": true}

func TestValidateActualsAcceptsAValidFile(t *testing.T) {
	if err := ValidateActuals(validFile(), "2026-08", known); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateActuals(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ActualsFile)
		wantErr string
	}{
		{
			name:    "month disagrees with the filename",
			mutate:  func(f *ActualsFile) { f.Month = "2026-07" },
			wantErr: "filename",
		},
		{
			name:    "duplicate transaction id",
			mutate:  func(f *ActualsFile) { f.Transactions[1].Id = "t1" },
			wantErr: "more than once",
		},
		{
			name:    "both category and ignored",
			mutate:  func(f *ActualsFile) { f.Transactions[0].Ignored = strp("salary") },
			wantErr: "exactly one",
		},
		{
			name: "neither category nor ignored",
			mutate: func(f *ActualsFile) {
				f.Transactions[0].Category = nil
			},
			wantErr: "undecided",
		},
		{
			name:    "unknown category id",
			mutate:  func(f *ActualsFile) { f.Transactions[0].Category = strp("nope.gone") },
			wantErr: "not in budget.json",
		},
		{
			name:    "zero amount",
			mutate:  func(f *ActualsFile) { f.Transactions[0].Amount = 0 },
			wantErr: "amount of 0",
		},
		{
			name:    "date outside the month",
			mutate:  func(f *ActualsFile) { f.Transactions[0].Date = "2026-09-01" },
			wantErr: "outside 2026-08",
		},
		{
			name:    "date is not a real day",
			mutate:  func(f *ActualsFile) { f.Transactions[0].Date = "2026-02-30" },
			wantErr: "not a real date",
		},
		{
			name:    "coverage reaches outside the month",
			mutate:  func(f *ActualsFile) { f.Coverage[0].To = "2026-09-02" },
			wantErr: "outside",
		},
		{
			name:    "coverage to precedes from",
			mutate:  func(f *ActualsFile) { f.Coverage[0].To = "2026-08-01"; f.Coverage[0].From = "2026-08-09" },
			wantErr: "precedes",
		},
		{
			name:   "coverage empty",
			mutate: func(f *ActualsFile) { f.Coverage = nil },
			// The schema's minItems catches this on the way in, but the write
			// path builds a document in Go and marshals it without ever
			// unmarshalling, so nothing would have stopped it committing
			// "coverage": null and breaking the file for the next reader.
			wantErr: "no coverage",
		},
		{
			name:    "month is not a real month",
			mutate:  func(f *ActualsFile) { f.Month = "2026-13" },
			wantErr: "not a real month",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := validFile()
			tt.mutate(&f)
			monthKey := "2026-08"
			if tt.name == "month is not a real month" {
				monthKey = "2026-13"
			}
			err := ValidateActuals(f, monthKey, known)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want an error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestValidateActualsReportsEveryBreach is why this validator accumulates
// instead of failing fast.
func TestValidateActualsReportsEveryBreach(t *testing.T) {
	f := validFile()
	f.Transactions[0].Amount = 0
	f.Transactions[0].Category = strp("nope.gone")
	f.Transactions[1].Date = "2026-09-15"

	err := ValidateActuals(f, "2026-08", known)
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"amount of 0", "not in budget.json", "outside 2026-08"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to also report %q", err, want)
		}
	}
}

// TestValidateActualsNilKnownIDsSkipsTheCrossCheck covers the runtime loader,
// which has no budget file to hand.
func TestValidateActualsNilKnownIDsSkipsTheCrossCheck(t *testing.T) {
	f := validFile()
	f.Transactions[0].Category = strp("something.unrecognised")
	if err := ValidateActuals(f, "2026-08", nil); err != nil {
		t.Fatalf("unexpected error with nil knownIDs: %v", err)
	}
}

func TestMonthKeyOf(t *testing.T) {
	tests := []struct{ in, want string }{
		{"2026-08.json", "2026-08"},
		{"2026-12.json", "2026-12"},
		{"short", ""},
	}
	for _, tt := range tests {
		if got := MonthKeyOf(tt.in); got != tt.want {
			t.Errorf("MonthKeyOf(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
