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

type Holidays struct {
	Country     string
	Subdivision string
	HTTP        *http.Client

	mu    sync.Mutex
	cache map[string]any
}

type Holiday struct {
	Date time.Time
	Name string
}

type localizedName struct {
	Language string `json:"language"`
	Text     string `json:"text"`
}

type Country struct {
	IsoCode string
	Name    string
}

type Subdivision struct {
	IsoCode string
	Name    string
}

func (h *Holidays) countryOrDefault() string {
	if h.Country == "" {
		return "AT"
	}
	return h.Country
}

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

func pickName(names []localizedName) string {
	return pickNameLang(names, "DE")
}

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
