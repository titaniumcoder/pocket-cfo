package tracker

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestHolidaysFetchExpandsAndSorts(t *testing.T) {
	// Two entries out of order; the second spans two days. German name preferred.
	body := `[
	  {"startDate":"2026-12-26","endDate":"2026-12-26","name":[{"language":"EN","text":"St. Stephen"},{"language":"DE","text":"Stefanitag"}]},
	  {"startDate":"2026-12-24","endDate":"2026-12-25","name":[{"language":"DE","text":"Weihnachten"}]}
	]`
	b := &fakeBackend{holidays: body}
	h := &Holidays{Subdivision: "AT-9", HTTP: b.transport()}

	got, err := h.Fetch(context.Background(), mar(1), mar(31))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d holiday days, want 3 (24,25,26): %+v", len(got), got)
	}
	wantDates := []string{"2026-12-24", "2026-12-25", "2026-12-26"}
	for i, w := range wantDates {
		if got[i].Date.Format("2006-01-02") != w {
			t.Errorf("day %d = %s, want %s (sorted)", i, got[i].Date.Format("2006-01-02"), w)
		}
	}
	if got[0].Name != "Weihnachten" || got[2].Name != "Stefanitag" {
		t.Errorf("names = %q, %q; want German variants", got[0].Name, got[2].Name)
	}
}

func TestHolidaysFetchCaches(t *testing.T) {
	calls := 0
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(`[]`, nil), nil
	})
	h := &Holidays{HTTP: &http.Client{Transport: rt}}

	for range 3 {
		if _, err := h.Fetch(context.Background(), mar(1), mar(31)); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("holidays endpoint hit %d times, want 1 (cached)", calls)
	}
}

func TestHolidaysFetchErrorStatus(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return statusResponse(503, "down"), nil
	})
	h := &Holidays{HTTP: &http.Client{Transport: rt}}
	if _, err := h.Fetch(context.Background(), mar(1), mar(31)); err == nil {
		t.Fatal("expected error on 503")
	}
}

func TestPickName(t *testing.T) {
	type nm = struct {
		Language string `json:"language"`
		Text     string `json:"text"`
	}
	if got := pickName([]nm{{"EN", "English"}, {"DE", "Deutsch"}}); got != "Deutsch" {
		t.Errorf("pickName preferred = %q, want Deutsch", got)
	}
	if got := pickName([]nm{{"FR", "Français"}, {"EN", "English"}}); got != "Français" {
		t.Errorf("pickName fallback = %q, want first (Français)", got)
	}
	if got := pickName(nil); got != "" {
		t.Errorf("pickName(nil) = %q, want empty", got)
	}
}

// ensure the expanded entries carry a sane time component (midnight).
func TestHolidayDateIsMidnight(t *testing.T) {
	b := &fakeBackend{holidays: `[{"startDate":"2026-05-01","endDate":"2026-05-01","name":[{"language":"DE","text":"Tag der Arbeit"}]}]`}
	h := &Holidays{HTTP: b.transport()}
	got, err := h.Fetch(context.Background(), mar(1), mar(31))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Date.Equal(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("holiday date = %v, want 2026-05-01 midnight UTC", got)
	}
}
