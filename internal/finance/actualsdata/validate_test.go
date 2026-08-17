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

func movp(m Movement) *Movement { return &m }

// TestAMovementMarkerNeedsAnIgnoredReasonBesideIt: the marker is a second,
// orthogonal axis, not a fourth disposition. A line that moved money between
// the owner and the company genuinely is not a budget expense, so it still
// says why like every other ignored line.
func TestAMovementMarkerNeedsAnIgnoredReasonBesideIt(t *testing.T) {
	f := validFile()
	f.Transactions = append(f.Transactions, Transaction{
		Id: "m1", Date: "2026-08-06", Description: "To Rico Metzger", Amount: 5000,
		Account: "Company Checking", Movement: movp(MovementOwnerDraw),
	})
	err := ValidateActuals(f, "2026-08", known)
	if err == nil {
		t.Fatal("a marked line with no ignored reason was accepted")
	}
	if !strings.Contains(err.Error(), "no ignored reason") {
		t.Errorf("error = %q, want it to name the missing reason", err)
	}
}

// TestAMarkedLineStillSatisfiesTheOneDispositionRule guards the regression the
// marker could most easily cause: folding it into the exactly-one-of count
// would make every marked line illegal.
func TestAMarkedLineStillSatisfiesTheOneDispositionRule(t *testing.T) {
	f := validFile()
	f.Transactions = append(f.Transactions, Transaction{
		Id: "m1", Date: "2026-08-06", Description: "To Rico Metzger", Amount: 5000,
		Account: "Company Checking", Ignored: strp("owner draw — the receiving side is on the private statement"),
		Movement: movp(MovementOwnerDraw),
	})
	if err := ValidateActuals(f, "2026-08", known); err != nil {
		t.Fatalf("a properly marked line was refused: %v", err)
	}
}

// TestTheMirrorLineOnThePrivateStatementCannotBeMarkedBecauseItsSignIsWrong is
// the whole of "record it once". Both statements are imported, so the same
// transfer arrives twice; the mirror's sign is the other way round, so the
// arithmetic refuses it without ever resolving account against accounts.json,
// which this file deliberately does not do.
func TestTheMirrorLineOnThePrivateStatementCannotBeMarkedBecauseItsSignIsWrong(t *testing.T) {
	f := validFile()
	f.Transactions = append(f.Transactions, Transaction{
		Id: "mirror", Date: "2026-08-06", Description: "From TITANIUM CODER EOOD", Amount: -5000,
		Account: "Private Checking", Ignored: strp("owner draw arriving"), Movement: movp(MovementOwnerDraw),
	})
	err := ValidateActuals(f, "2026-08", known)
	if err == nil {
		t.Fatal("the private-statement mirror of a draw was accepted, so the transfer would count twice")
	}
	if !strings.Contains(err.Error(), "mark the company side instead") {
		t.Errorf("error = %q, want it to say where the marker belongs", err)
	}
}

// TestMoneyPaidIntoTheCompanyIsMarkedOnACredit: the direction reverses with
// the movement, so a contribution is refused on a debit and accepted on the
// credit the company statement actually shows.
func TestMoneyPaidIntoTheCompanyIsMarkedOnACredit(t *testing.T) {
	withAmount := func(amount float64) error {
		f := validFile()
		f.Transactions = append(f.Transactions, Transaction{
			Id: "c1", Date: "2026-08-03", Description: "From Rico M", Amount: amount,
			Account: "Company Checking", Ignored: strp("money paid into the company account"),
			Movement: movp(MovementOwnerContribution),
		})
		return ValidateActuals(f, "2026-08", known)
	}
	if err := withAmount(-500); err != nil {
		t.Errorf("a contribution on the company statement's credit was refused: %v", err)
	}
	if withAmount(500) == nil {
		t.Error("a contribution marked on money leaving the company was accepted")
	}
}

// TestATaxPaymentIsMarkedButIsNotACrossing: corporate and dividend tax leave
// the company but never reach the owner, so they are marked only to earn their
// place on the spending page and must move no balance.
func TestATaxPaymentIsMarkedButIsNotACrossing(t *testing.T) {
	crossing := map[Movement]bool{
		MovementSalaryTransfer:    true,
		MovementOwnerDraw:         true,
		MovementDividendPayout:    true,
		MovementOwnerContribution: true,
		MovementCorporateTax:      false,
		MovementDividendTax:       false,
	}
	for m, want := range crossing {
		if got := (Part{Movement: m}).Crossed(); got != want {
			t.Errorf("%s crossed = %v, want %v", m, got, want)
		}
	}
}

// TestEveryMovementValueIsClassifiedAsACrossingOrNot: Crossed defaults to
// false, so a value added to the schema later is silently inert rather than
// silently signed wrong. This fails until somebody decides which it is.
func TestEveryMovementValueIsClassifiedAsACrossingOrNot(t *testing.T) {
	classified := map[Movement]bool{
		MovementSalaryTransfer:    true,
		MovementOwnerDraw:         true,
		MovementDividendPayout:    true,
		MovementOwnerContribution: true,
		MovementCorporateTax:      true,
		MovementDividendTax:       true,
	}
	for _, v := range enumValues_Movement {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("movement enum holds a non-string %v", v)
		}
		if !classified[Movement(s)] {
			t.Errorf("movement %q is in the schema but nothing decides whether it crosses between the owner and the company", s)
		}
	}
}

// TestASplitPartCarriesItsOwnMovement: one statement line can be part expense
// and part transfer, and PartsOf is what every consumer walks.
func TestASplitPartCarriesItsOwnMovement(t *testing.T) {
	tx := Transaction{
		Id: "s1", Date: "2026-08-06", Description: "MIXED", Amount: 5100, Account: "Company Checking",
		Splits: []Split{
			{Amount: 100, Category: strp("food.groceries")},
			{Amount: 5000, Ignored: strp("owner draw"), Movement: movp(MovementOwnerDraw)},
		},
	}
	parts := PartsOf(tx)
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(parts))
	}
	if parts[0].Crossed() {
		t.Error("the grocery part reads as money crossing to the owner")
	}
	if !parts[1].Crossed() || parts[1].Movement != MovementOwnerDraw {
		t.Errorf("the transfer part lost its marker: %+v", parts[1])
	}

	f := validFile()
	f.Transactions = append(f.Transactions, tx)
	if err := ValidateActuals(f, "2026-08", known); err != nil {
		t.Fatalf("a split with one marked part was refused: %v", err)
	}
}

// TestAMovementOnTheLineAndOnAPartIsRefusedBecauseThePartsDecide mirrors the
// rule splits already follow for dispositions.
func TestAMovementOnTheLineAndOnAPartIsRefusedBecauseThePartsDecide(t *testing.T) {
	f := validFile()
	f.Transactions = append(f.Transactions, Transaction{
		Id: "s2", Date: "2026-08-06", Description: "MIXED", Amount: 5100, Account: "Company Checking",
		Movement: movp(MovementOwnerDraw),
		Splits: []Split{
			{Amount: 100, Category: strp("food.groceries")},
			{Amount: 5000, Ignored: strp("owner draw"), Movement: movp(MovementOwnerDraw)},
		},
	})
	err := ValidateActuals(f, "2026-08", known)
	if err == nil {
		t.Fatal("a line marked as well as its parts was accepted")
	}
	if !strings.Contains(err.Error(), "the parts decide") {
		t.Errorf("error = %q, want the same wording splits already use", err)
	}
}
