package tracker

import (
	"context"
	"testing"
	"time"
)

// TestADividendIsFoundByItsMonthAndNotItsDay: the day on a dividend is
// informational, the same convention a one-off category's date follows. What
// decides which month is charged is the month, so a distribution dated on the
// 30th and one dated on the 1st of the same month are the same month's money.
func TestADividendIsFoundByItsMonthAndNotItsDay(t *testing.T) {
	trk := actualsTracker(t, nil)
	trk.Budget = newTestBudget(t, map[string]string{"budget.json": `{"groups":[
		{"name":"Housing","kind":"private","categories":[{"id":"00000000-0000-4000-8000-000000000001","name":"Rent","amount":1000}]}
	],"dividends":[{"date":"2026-09-30","amount":10000,"note":"2025 profit"}]}`})

	bv, err := trk.Budget.ForMonth(context.Background(), 2026, time.September, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if due := bv.Dividends.dueIn(yearMonth{2026, time.September}); due.AmountEUR != 10000 {
		t.Errorf("September is due %v, want the 10000 dated on its 30th", due.AmountEUR)
	}
	if due := bv.Dividends.dueIn(yearMonth{2026, time.October}); due.AmountEUR != 0 {
		t.Errorf("October is due %v — the dividend belongs to September alone", due.AmountEUR)
	}
	// The plan travels whole, not pre-filtered: the roll-forward walks other
	// months with this same view and each one picks its own out.
	bvAugust, err := trk.Budget.ForMonth(context.Background(), 2026, time.August, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if due := bvAugust.Dividends.dueIn(yearMonth{2026, time.September}); due.AmountEUR != 10000 {
		t.Error("August's view cannot see September's dividend, so a walked month would close the company too high")
	}
}

// TestTwoDividendsInOneMonthAreSummed: the file states what was distributed,
// and the month is charged the whole of it — with both days kept, so the page
// can be reconciled line by line against the file.
func TestTwoDividendsInOneMonthAreSummed(t *testing.T) {
	trk := actualsTracker(t, nil)
	trk.Budget = newTestBudget(t, map[string]string{"budget.json": `{"groups":[
		{"name":"Housing","kind":"private","categories":[{"id":"00000000-0000-4000-8000-000000000001","name":"Rent","amount":1000}]}
	],"dividends":[{"date":"2026-09-30","amount":4000},{"date":"2026-09-15","amount":6000}]}`})

	bv, err := trk.Budget.ForMonth(context.Background(), 2026, time.September, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	due := bv.Dividends.dueIn(yearMonth{2026, time.September})
	if due.AmountEUR != 10000 {
		t.Errorf("September is due %v, want 4000 and 6000 summed", due.AmountEUR)
	}
	// Sorted by date, so the page does not reorder itself when the file does.
	if len(due.Days) != 2 || due.Days[0] != "2026-09-15" || due.Days[1] != "2026-09-30" {
		t.Errorf("days = %v, want both, earliest first", due.Days)
	}
}
