package tracker

import (
	"context"
	"net/http"
	"strings"
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
	if got := pickName([]localizedName{{"EN", "English"}, {"DE", "Deutsch"}}); got != "Deutsch" {
		t.Errorf("pickName preferred = %q, want Deutsch", got)
	}
	if got := pickName([]localizedName{{"FR", "Français"}, {"EN", "English"}}); got != "Français" {
		t.Errorf("pickName fallback = %q, want first (Français)", got)
	}
	if got := pickName(nil); got != "" {
		t.Errorf("pickName(nil) = %q, want empty", got)
	}
}

func TestPickNameLangPrefersRequestedLanguage(t *testing.T) {
	names := []localizedName{{"DE", "Deutsch"}, {"EN", "English"}}
	if got := pickNameLang(names, "EN"); got != "English" {
		t.Errorf("pickNameLang(EN) = %q, want English", got)
	}
	if got := pickNameLang(names, "FR"); got != "Deutsch" {
		t.Errorf("pickNameLang(FR, not present) = %q, want first entry (Deutsch)", got)
	}
}

func TestHolidaysCountries(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Path, "/Countries") {
			return statusResponse(http.StatusNotFound, "unexpected: "+r.URL.String()), nil
		}
		return jsonResponse(`[
		  {"isoCode":"AT","name":[{"language":"DE","text":"Österreich"},{"language":"EN","text":"Austria"}]},
		  {"isoCode":"BG","name":[{"language":"EN","text":"Bulgaria"}]}
		]`, nil), nil
	})
	h := &Holidays{HTTP: &http.Client{Transport: rt}}

	got, err := h.Countries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Country{{IsoCode: "AT", Name: "Austria"}, {IsoCode: "BG", Name: "Bulgaria"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Countries() = %+v, want %+v (English name preferred, sorted by name)", got, want)
	}
}

func TestHolidaysCountriesCaches(t *testing.T) {
	calls := 0
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(`[]`, nil), nil
	})
	h := &Holidays{HTTP: &http.Client{Transport: rt}}

	for range 3 {
		if _, err := h.Countries(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("countries endpoint hit %d times, want 1 (cached)", calls)
	}
}

func TestHolidaysCountriesErrorStatus(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return statusResponse(503, "down"), nil
	})
	h := &Holidays{HTTP: &http.Client{Transport: rt}}
	if _, err := h.Countries(context.Background()); err == nil {
		t.Fatal("expected error on 503")
	}
}

func TestHolidaysSubdivisions(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Path, "/Subdivisions") || r.URL.Query().Get("countryIsoCode") != "AT" {
			return statusResponse(http.StatusNotFound, "unexpected: "+r.URL.String()), nil
		}
		return jsonResponse(`[
		  {"isoCode":"AT-9","name":[{"language":"EN","text":"Vienna"}]},
		  {"isoCode":"AT-1","name":[{"language":"EN","text":"Burgenland"}]}
		]`, nil), nil
	})
	h := &Holidays{HTTP: &http.Client{Transport: rt}}

	got, err := h.Subdivisions(context.Background(), "AT")
	if err != nil {
		t.Fatal(err)
	}
	want := []Subdivision{{IsoCode: "AT-1", Name: "Burgenland"}, {IsoCode: "AT-9", Name: "Vienna"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Subdivisions(AT) = %+v, want %+v (sorted by name)", got, want)
	}
}

func TestHolidaysSubdivisionsEmptyForCountryWithNone(t *testing.T) {
	// OpenHolidays returns 200 with [] for a country it tracks with no
	// subdivision granularity — not an error.
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`[]`, nil), nil
	})
	h := &Holidays{HTTP: &http.Client{Transport: rt}}

	got, err := h.Subdivisions(context.Background(), "AD")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("Subdivisions(AD) = %+v, want empty", got)
	}
}

func TestHolidaysSubdivisionsErrorStatus(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return statusResponse(503, "down"), nil
	})
	h := &Holidays{HTTP: &http.Client{Transport: rt}}
	if _, err := h.Subdivisions(context.Background(), "AT"); err == nil {
		t.Fatal("expected error on 503")
	}
}

func TestHolidaysCountryOrDefault(t *testing.T) {
	if got := (&Holidays{}).countryOrDefault(); got != "AT" {
		t.Errorf("empty Country = %q, want AT default", got)
	}
	if got := (&Holidays{Country: "BG"}).countryOrDefault(); got != "BG" {
		t.Errorf("Country=BG = %q, want BG", got)
	}
}

// TestHolidaysFetchCacheKeyIncludesCountry confirms two different Country
// values are cached independently, not sharing one entry — without this a
// second country's holidays would incorrectly serve the first country's
// cached response.
func TestHolidaysFetchCacheKeyIncludesCountry(t *testing.T) {
	calls := map[string]int{}
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls[r.URL.Query().Get("countryIsoCode")]++
		return jsonResponse(`[]`, nil), nil
	})
	client := &http.Client{Transport: rt}

	at := &Holidays{Country: "AT", HTTP: client}
	bg := &Holidays{Country: "BG", HTTP: client}
	if _, err := at.Fetch(context.Background(), mar(1), mar(31)); err != nil {
		t.Fatal(err)
	}
	if _, err := bg.Fetch(context.Background(), mar(1), mar(31)); err != nil {
		t.Fatal(err)
	}
	if calls["AT"] != 1 || calls["BG"] != 1 {
		t.Errorf("calls = %+v, want one fetch per country", calls)
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
