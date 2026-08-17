package accountsdata

import (
	"strings"
	"testing"
)

func fileWith(readings ...Reading) AccountsFile {
	return AccountsFile{Accounts: []Account{{Name: "P", Kind: AccountKindPrivate, Balances: readings}}}
}

func reading(asOf string) Reading {
	return Reading{AsOf: asOf, Balance: 100}
}

// TestAReadingMustCloseItsMonth is the rule the whole month hangs on. A
// balance is what a month CLOSED on and therefore what the next one opens
// with, and only the month is ever read off the date — so a figure dated
// mid-month is filed as that month's close while the rest of the month is
// still to be spent, and every month after it opens on money already gone.
// Refusing the file is the point: it is wrong everywhere it is read, not just
// where it was written.
func TestAReadingMustCloseItsMonth(t *testing.T) {
	for _, asOf := range []string{"2026-08-01", "2026-08-14", "2026-08-30", "2026-02-27", "2024-02-28"} {
		t.Run(asOf, func(t *testing.T) {
			err := ValidateAccounts(fileWith(reading(asOf)))
			if err == nil {
				t.Fatalf("%s was accepted", asOf)
			}
			for _, want := range []string{"P", asOf, "last day"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to mention %q", err, want)
				}
			}
		})
	}
}

// TestTheLastDayOfAMonthIsAccepted keeps the other half honest — including
// the two Februaries, where a rule written as "the 31st" or "the 30th" would
// refuse a perfectly good reading.
func TestTheLastDayOfAMonthIsAccepted(t *testing.T) {
	for _, asOf := range []string{"2026-01-31", "2026-02-28", "2024-02-29", "2026-04-30", "2026-06-30", "2026-11-30", "2026-12-31"} {
		t.Run(asOf, func(t *testing.T) {
			if err := ValidateAccounts(fileWith(reading(asOf))); err != nil {
				t.Errorf("%s was refused: %v", asOf, err)
			}
		})
	}
}

// TestTwoReadingsInOneMonthAreStillRefused: with every reading dated a month
// end, the only way to write two for one month is to date them identically —
// still two candidate openings for the next month, still refused.
func TestTwoReadingsInOneMonthAreStillRefused(t *testing.T) {
	err := ValidateAccounts(fileWith(reading("2026-07-31"), Reading{AsOf: "2026-07-31", Balance: 250}))
	if err == nil {
		t.Fatal("one month closed on two figures and the file was accepted")
	}
	if !strings.Contains(err.Error(), "2026-07") {
		t.Errorf("error = %q, want it to name the month", err)
	}
}

func TestAnUnparseableDateIsRefused(t *testing.T) {
	if err := ValidateAccounts(fileWith(reading("2026-02-31"))); err == nil {
		t.Error("31 February was accepted")
	}
}

func TestAnAccountWithNoReadingsIsRefused(t *testing.T) {
	t.Run("an account", func(t *testing.T) {
		err := ValidateAccounts(fileWith())
		if err == nil {
			t.Fatal("an account with no readings was accepted")
		}
		for _, want := range []string{"P", "no readings"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to contain %q", err, want)
			}
		}
	})

	t.Run("the director's loan", func(t *testing.T) {
		f := fileWith(reading("2026-07-31"))
		f.DirectorLoan = &AccountsFileDirectorLoan{}
		if err := ValidateAccounts(f); err == nil {
			t.Error("a director's loan with no readings was accepted")
		}
	})
}

func TestTwoAccountsWithTheSameNameAreRefused(t *testing.T) {
	f := AccountsFile{Accounts: []Account{
		{Name: "P", Kind: AccountKindPrivate, Balances: []Reading{reading("2026-07-31")}},
		{Name: "P", Kind: AccountKindCompany, Balances: []Reading{reading("2026-07-31")}},
	}}
	if err := ValidateAccounts(f); err == nil {
		t.Error("one name over two histories was accepted")
	}
}
