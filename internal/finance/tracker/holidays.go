package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Holidays is an abstraction over the OpenHolidays API — no API key needed,
// so unlike Toggl/api2pdf it's never "not configured," only optionally
// pointed at a country/subdivision other than the AT default.
// Successful responses are cached in memory forever (a given month's
// holidays, or the reference list of countries/subdivisions, don't change).
type Holidays struct {
	Country     string // optional, e.g. "AT"; falls back to "AT" when empty, see countryOrDefault
	Subdivision string // optional, e.g. "AT-9" for Vienna
	HTTP        *http.Client

	mu    sync.Mutex
	cache map[string]any
}

// Holiday is a single public-holiday day.
type Holiday struct {
	Date time.Time
	Name string
}

// localizedName is the OpenHolidays API's [{language, text}, ...] shape,
// used for country/subdivision/holiday names alike.
type localizedName struct {
	Language string `json:"language"`
	Text     string `json:"text"`
}

// Country is one country OpenHolidays knows about — see Countries, used by
// the /info diagnostics page to help pick config.json's holidayCountry.
type Country struct {
	IsoCode string
	Name    string
}

// Subdivision is one country's subdivision (state/region/etc.) — see
// Subdivisions. IsoCode is what config.json's holidaySubdivision expects
// (e.g. "AT-9").
type Subdivision struct {
	IsoCode string
	Name    string
}

// countryOrDefault is "AT" when Country is unset — the default this app
// shipped with before holidayCountry existed, kept so existing configs and
// every test constructing a bare &Holidays{...} keep working unchanged.
func (h *Holidays) countryOrDefault() string {
	if h.Country == "" {
		return "AT"
	}
	return h.Country
}

// Fetch returns the public holidays in [start, end], one entry per calendar day
// (multi-day holidays are expanded), sorted by date.
func (h *Holidays) Fetch(ctx context.Context, start, end time.Time) ([]Holiday, error) {
	cacheKey := "holidays|" + h.countryOrDefault() + "|" + h.Subdivision + "|" + start.Format("2006-01-02") + "|" + end.Format("2006-01-02")
	v, err := h.getCached(cacheKey, func() (any, error) {
		return h.fetchHolidays(ctx, start, end)
	})
	if err != nil {
		return nil, err
	}
	return v.([]Holiday), nil
}

func (h *Holidays) fetchHolidays(ctx context.Context, start, end time.Time) ([]Holiday, error) {
	q := url.Values{}
	q.Set("countryIsoCode", h.countryOrDefault())
	q.Set("languageIsoCode", "DE")
	q.Set("validFrom", start.Format("2006-01-02"))
	q.Set("validTo", end.Format("2006-01-02"))
	if h.Subdivision != "" {
		q.Set("subdivisionCode", h.Subdivision)
	}

	endpoint := "https://openholidaysapi.org/PublicHolidays?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := h.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("openholidays: status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var entries []struct {
		StartDate string          `json:"startDate"`
		EndDate   string          `json:"endDate"`
		Name      []localizedName `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("openholidays: decode: %w", err)
	}

	var holidays []Holiday
	for _, e := range entries {
		from, err1 := time.Parse("2006-01-02", e.StartDate)
		to, err2 := time.Parse("2006-01-02", e.EndDate)
		if err1 != nil || err2 != nil {
			continue
		}
		name := pickName(e.Name)
		for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
			holidays = append(holidays, Holiday{Date: d, Name: name})
		}
	}
	sort.Slice(holidays, func(i, j int) bool { return holidays[i].Date.Before(holidays[j].Date) })
	return holidays, nil
}

// Countries returns every country OpenHolidays knows about, cached forever
// (this reference list doesn't change at runtime) — for the /info
// diagnostics page, to help pick config.json's holidayCountry.
func (h *Holidays) Countries(ctx context.Context) ([]Country, error) {
	v, err := h.getCached("countries", func() (any, error) {
		return h.fetchCountries(ctx)
	})
	if err != nil {
		return nil, err
	}
	return v.([]Country), nil
}

func (h *Holidays) fetchCountries(ctx context.Context) ([]Country, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openholidaysapi.org/Countries", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := h.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("openholidays: countries status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var entries []struct {
		IsoCode string          `json:"isoCode"`
		Name    []localizedName `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("openholidays: decode countries: %w", err)
	}

	out := make([]Country, len(entries))
	for i, e := range entries {
		out[i] = Country{IsoCode: e.IsoCode, Name: pickNameLang(e.Name, "EN")}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Subdivisions returns countryIsoCode's subdivisions (state/region/etc.),
// cached forever per country — an empty result (200 with []) is normal for
// a country OpenHolidays tracks with no subdivision granularity.
func (h *Holidays) Subdivisions(ctx context.Context, countryIsoCode string) ([]Subdivision, error) {
	v, err := h.getCached("subdivisions|"+countryIsoCode, func() (any, error) {
		return h.fetchSubdivisions(ctx, countryIsoCode)
	})
	if err != nil {
		return nil, err
	}
	return v.([]Subdivision), nil
}

func (h *Holidays) fetchSubdivisions(ctx context.Context, countryIsoCode string) ([]Subdivision, error) {
	q := url.Values{}
	q.Set("countryIsoCode", countryIsoCode)
	endpoint := "https://openholidaysapi.org/Subdivisions?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := h.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("openholidays: subdivisions status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var entries []struct {
		IsoCode string          `json:"isoCode"`
		Name    []localizedName `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("openholidays: decode subdivisions: %w", err)
	}

	out := make([]Subdivision, len(entries))
	for i, e := range entries {
		out[i] = Subdivision{IsoCode: e.IsoCode, Name: pickNameLang(e.Name, "EN")}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// getCached returns the cached value for key, or computes it via fn and
// stores it forever — same unconditional-forever-cache convention as
// Toggl.getCached, just without the date-range eviction tagging Toggl needs
// (nothing here is ever evicted: holidays/countries/subdivisions are all
// reference data that doesn't change during the process lifetime).
func (h *Holidays) getCached(key string, fn func() (any, error)) (any, error) {
	if v, ok := h.cached(key); ok {
		log.Printf("holidays: %s — served from cache", key)
		return v, nil
	}

	log.Printf("holidays: %s — fetching…", key)
	t0 := time.Now()
	v, err := fn()
	elapsed := time.Since(t0).Round(time.Millisecond)
	if err != nil {
		log.Printf("holidays: %s — failed after %s: %v", key, elapsed, err)
		return nil, err
	}
	log.Printf("holidays: %s — fetched in %s", key, elapsed)
	h.store(key, v)
	return v, nil
}

func (h *Holidays) cached(key string) (any, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.cache[key]
	return v, ok
}

func (h *Holidays) store(key string, v any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cache == nil {
		h.cache = map[string]any{}
	}
	h.cache[key] = v
}

// pickName returns the German name if present, otherwise the first available.
func pickName(names []localizedName) string {
	return pickNameLang(names, "DE")
}

// pickNameLang returns the name in the given language if present, otherwise
// the first available.
func pickNameLang(names []localizedName, lang string) string {
	for _, n := range names {
		if strings.EqualFold(n.Language, lang) {
			return n.Text
		}
	}
	if len(names) > 0 {
		return names[0].Text
	}
	return ""
}

func (h *Holidays) client() *http.Client {
	if h.HTTP != nil {
		return h.HTTP
	}
	return http.DefaultClient
}
