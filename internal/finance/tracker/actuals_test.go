package tracker

import (
	"context"
	"testing"
	"testing/fstest"
	"time"
)

const testActualsAugust = `{
  "month": "2026-08",
  "coverage": [
    { "account": "Private Checking", "from": "2026-08-01", "to": "2026-08-09", "imported_at": "2026-08-10" }
  ],
  "transactions": [
    { "id": "t1", "date": "2026-08-01", "description": "MIETE", "amount": 900, "account": "Private Checking", "category": "housing.rent" },
    { "id": "t2", "date": "2026-08-03", "description": "LIDL", "amount": 210.4, "account": "Private Checking", "category": "food.groceries" },
    { "id": "t3", "date": "2026-08-07", "description": "LIDL RETOURE", "amount": -12.6, "account": "Private Checking", "category": "food.groceries" },
    { "id": "t4", "date": "2026-08-02", "description": "SALARY", "amount": -2400, "account": "Private Checking", "ignored": "salary" }
  ]
}`

func newTestActuals(t *testing.T, files map[string]string) *Actuals {
	t.Helper()
	mfs := fstest.MapFS{}
	for path, body := range files {
		mfs[path] = &fstest.MapFile{Data: []byte(body)}
	}
	return &Actuals{FS: mfs}
}

func TestActualsMissingFileIsNotAnError(t *testing.T) {
	a := newTestActuals(t, nil)
	got, err := a.ForMonth(context.Background(), 2026, time.August)
	if err != nil {
		t.Fatalf("ForMonth(missing) = %v, want no error — an unreconciled month is absence, not failure", err)
	}
	if got.Present {
		t.Error("Present = true, want false for a month with no file")
	}
}

func TestActualsNilIsSafe(t *testing.T) {
	var a *Actuals
	if _, err := a.ForMonth(context.Background(), 2026, time.August); err != nil {
		t.Errorf("nil receiver: %v", err)
	}
	if _, err := a.ForYear(context.Background(), 2026, time.Time{}); err != nil {
		t.Errorf("nil receiver ForYear: %v", err)
	}
	if _, err := a.ChargedMonths(context.Background(), 2026, time.Time{}); err != nil {
		t.Errorf("nil receiver ChargedMonths: %v", err)
	}
	a.Evict() // must not panic

	unconfigured := &Actuals{}
	if _, err := unconfigured.ForMonth(context.Background(), 2026, time.August); err != nil {
		t.Errorf("nil FS: %v", err)
	}
}

func TestActualsForMonth(t *testing.T) {
	a := newTestActuals(t, map[string]string{"actuals/2026-08.json": testActualsAugust})
	got, err := a.ForMonth(context.Background(), 2026, time.August)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Present {
		t.Fatal("Present = false, want true")
	}
	if want := 90000; got.ByCategory["housing.rent"] != want {
		t.Errorf("housing.rent = %d, want %d", got.ByCategory["housing.rent"], want)
	}
	// 210.40 spent minus a 12.60 refund, in cents.
	if want := 19780; got.ByCategory["food.groceries"] != want {
		t.Errorf("food.groceries = %d, want %d (a refund reduces its own category)", got.ByCategory["food.groceries"], want)
	}
	if want := 90000 + 19780; got.TotalCents != want {
		t.Errorf("TotalCents = %d, want %d", got.TotalCents, want)
	}
	// The ignored salary credit is -2400: if it leaked in, the total would be
	// wildly negative and the category map would carry an empty key.
	if _, ok := got.ByCategory[""]; ok {
		t.Error("an ignored line reached ByCategory")
	}
}

func TestActualsCoverage(t *testing.T) {
	tests := []struct {
		name     string
		coverage string
		complete bool
	}{
		{
			name:     "partial month",
			coverage: `{ "account": "A", "from": "2026-08-01", "to": "2026-08-09", "imported_at": "2026-08-10" }`,
		},
		{
			name:     "whole month, one range",
			coverage: `{ "account": "A", "from": "2026-08-01", "to": "2026-08-31", "imported_at": "2026-09-01" }`,
			complete: true,
		},
		{
			name: "two ranges that meet",
			coverage: `{ "account": "A", "from": "2026-08-01", "to": "2026-08-15", "imported_at": "2026-08-16" },
			           { "account": "A", "from": "2026-08-16", "to": "2026-08-31", "imported_at": "2026-09-01" }`,
			complete: true,
		},
		{
			name: "two ranges with a gap",
			coverage: `{ "account": "A", "from": "2026-08-01", "to": "2026-08-14", "imported_at": "2026-08-16" },
			           { "account": "A", "from": "2026-08-16", "to": "2026-08-31", "imported_at": "2026-09-01" }`,
		},
		{
			name: "overlapping ranges still complete",
			coverage: `{ "account": "A", "from": "2026-08-01", "to": "2026-08-20", "imported_at": "2026-08-21" },
			           { "account": "A", "from": "2026-08-10", "to": "2026-08-31", "imported_at": "2026-09-01" }`,
			complete: true,
		},
		{
			name: "one of two accounts stops early",
			coverage: `{ "account": "A", "from": "2026-08-01", "to": "2026-08-31", "imported_at": "2026-09-01" },
			           { "account": "B", "from": "2026-08-01", "to": "2026-08-09", "imported_at": "2026-08-10" }`,
		},
		{
			name:     "starts late",
			coverage: `{ "account": "A", "from": "2026-08-02", "to": "2026-08-31", "imported_at": "2026-09-01" }`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"month":"2026-08","coverage":[` + tt.coverage + `],"transactions":[]}`
			a := newTestActuals(t, map[string]string{"actuals/2026-08.json": body})
			got, err := a.ForMonth(context.Background(), 2026, time.August)
			if err != nil {
				t.Fatal(err)
			}
			if got.Complete != tt.complete {
				t.Errorf("Complete = %v, want %v", got.Complete, tt.complete)
			}
		})
	}
}

func TestActualsForYear(t *testing.T) {
	a := newTestActuals(t, map[string]string{
		"actuals/2026-08.json": testActualsAugust,
		"actuals/2026-09.json": `{"month":"2026-09","coverage":[{"account":"A","from":"2026-09-01","to":"2026-09-30","imported_at":"2026-10-01"}],
			"transactions":[{"id":"s1","date":"2026-09-02","description":"MIETE","amount":900,"account":"A","category":"housing.rent"}]}`,
	})
	got, err := a.ForYear(context.Background(), 2026, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Present {
		t.Fatal("Present = false, want true")
	}
	if want := 180000; got.ByCategory["housing.rent"] != want {
		t.Errorf("housing.rent = %d, want %d (both months summed)", got.ByCategory["housing.rent"], want)
	}
	if got.Complete {
		t.Error("Complete = true with 2 of 12 months present, want false")
	}
}

func TestActualsForYearEmpty(t *testing.T) {
	a := newTestActuals(t, nil)
	got, err := a.ForYear(context.Background(), 2026, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Present {
		t.Error("Present = true for a year with no files, want false")
	}
}

// TestActualsChargedMonths covers the lookup the mistimed-one-off check needs:
// a cost budgeted for one month and paid in another is invisible from either
// month alone.
func TestActualsChargedMonths(t *testing.T) {
	a := newTestActuals(t, map[string]string{
		"actuals/2026-07.json": `{"month":"2026-07","coverage":[{"account":"A","from":"2026-07-01","to":"2026-07-31","imported_at":"2026-08-01"}],
			"transactions":[{"id":"j1","date":"2026-07-05","description":"NOTEBOOK","amount":1800,"account":"A","category":"company-equipment.laptop"}]}`,
		"actuals/2026-08.json": testActualsAugust,
	})
	got, err := a.ChargedMonths(context.Background(), 2026, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []time.Month{time.July}; len(got["company-equipment.laptop"]) != 1 || got["company-equipment.laptop"][0] != want[0] {
		t.Errorf("company-equipment.laptop charged in %v, want %v", got["company-equipment.laptop"], want)
	}
	if len(got["food.groceries"]) != 1 || got["food.groceries"][0] != time.August {
		t.Errorf("food.groceries charged in %v, want [August]", got["food.groceries"])
	}
	// Two August grocery lines must not produce August twice.
	if len(got["food.groceries"]) != 1 {
		t.Errorf("a category charged twice in one month should list that month once, got %v", got["food.groceries"])
	}
}

func TestActualsCacheAndEvict(t *testing.T) {
	mfs := fstest.MapFS{"actuals/2026-08.json": &fstest.MapFile{Data: []byte(testActualsAugust)}}
	a := &Actuals{FS: mfs}

	if _, err := a.ForMonth(context.Background(), 2026, time.August); err != nil {
		t.Fatal(err)
	}
	// Change the file underneath: the cached value must survive until evicted.
	mfs["actuals/2026-08.json"] = &fstest.MapFile{Data: []byte(`{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],"transactions":[]}`)}

	cached, err := a.ForMonth(context.Background(), 2026, time.August)
	if err != nil {
		t.Fatal(err)
	}
	if cached.TotalCents == 0 {
		t.Error("the cache was bypassed — the changed file was read before Evict")
	}

	a.Evict()
	fresh, err := a.ForMonth(context.Background(), 2026, time.August)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.TotalCents != 0 {
		t.Errorf("TotalCents = %d after Evict, want 0 from the rewritten file", fresh.TotalCents)
	}
}

func TestActualsRejectsAMonthThatDoesNotMatchItsFilename(t *testing.T) {
	a := newTestActuals(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-07","coverage":[{"account":"A","from":"2026-07-01","to":"2026-07-31","imported_at":"2026-08-01"}],"transactions":[]}`,
	})
	if _, err := a.ForMonth(context.Background(), 2026, time.August); err == nil {
		t.Error("a file whose month disagrees with its filename must be an error — it means an import was saved over the wrong month")
	}
}

func TestActualsRounding(t *testing.T) {
	a := newTestActuals(t, map[string]string{"actuals/2026-08.json": `{
		"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[{"id":"r1","date":"2026-08-01","description":"X","amount":42.185,"account":"A","category":"food.groceries"}]}`})
	got, err := a.ForMonth(context.Background(), 2026, time.August)
	if err != nil {
		t.Fatal(err)
	}
	if want := 4219; got.ByCategory["food.groceries"] != want {
		t.Errorf("42.185 euros = %d cents, want %d", got.ByCategory["food.groceries"], want)
	}
}
