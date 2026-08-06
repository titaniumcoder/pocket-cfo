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

// Holidays is an abstraction over the OpenHolidays API for Austrian public
// holidays. Successful responses are cached in memory forever (a given month's
// holidays don't change), keyed by subdivision + date range.
type Holidays struct {
	Subdivision string // optional, e.g. "AT-9" for Vienna
	HTTP        *http.Client

	mu    sync.Mutex
	cache map[string][]Holiday
}

// Holiday is a single public-holiday day.
type Holiday struct {
	Date time.Time
	Name string
}

// Fetch returns the public holidays in [start, end], one entry per calendar day
// (multi-day holidays are expanded), sorted by date.
func (h *Holidays) Fetch(ctx context.Context, start, end time.Time) (result []Holiday, err error) {
	cacheKey := h.Subdivision + "|" + start.Format("2006-01-02") + "|" + end.Format("2006-01-02")
	if cached, ok := h.cached(cacheKey); ok {
		log.Printf("holidays: %s — served from cache", cacheKey)
		return cached, nil
	}

	log.Printf("holidays: %s — fetching…", cacheKey)
	t0 := time.Now()
	defer func() {
		elapsed := time.Since(t0).Round(time.Millisecond)
		if err != nil {
			log.Printf("holidays: %s — failed after %s: %v", cacheKey, elapsed, err)
		} else {
			log.Printf("holidays: %s — fetched in %s", cacheKey, elapsed)
		}
	}()

	q := url.Values{}
	q.Set("countryIsoCode", "AT")
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
		StartDate string `json:"startDate"`
		EndDate   string `json:"endDate"`
		Name      []struct {
			Language string `json:"language"`
			Text     string `json:"text"`
		} `json:"name"`
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
	h.store(cacheKey, holidays)
	return holidays, nil
}

func (h *Holidays) cached(key string) ([]Holiday, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.cache[key]
	return v, ok
}

func (h *Holidays) store(key string, holidays []Holiday) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cache == nil {
		h.cache = map[string][]Holiday{}
	}
	h.cache[key] = holidays
}

// pickName returns the German name if present, otherwise the first available.
func pickName(names []struct {
	Language string `json:"language"`
	Text     string `json:"text"`
}) string {
	for _, n := range names {
		if strings.EqualFold(n.Language, "DE") {
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
