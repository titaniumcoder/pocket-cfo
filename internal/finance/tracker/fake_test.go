package tracker

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain silences the external-call logging (it is verified indirectly via
// behavior) and collapses the Toggl retry backoff, which several tests would
// otherwise pay in real seconds. Retries are asserted by attempt count rather
// than elapsed time, so this costs no coverage.
func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	togglBackoffBase = time.Millisecond
	os.Exit(m.Run())
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// jsonResponse builds a 200 JSON response with the given headers.
func jsonResponse(body string, headers map[string]string) *http.Response {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
	resp.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		resp.Header.Set(k, v)
	}
	return resp
}

func statusResponse(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

// fakeBackend routes Toggl and OpenHolidays requests to canned responses. Any
// field left nil yields an empty JSON array.
type fakeBackend struct {
	// detailed is consulted per detailed-report POST. It receives the 0-based
	// page index and returns the JSON body plus the X-Next-Row-Number header
	// (empty string = last page).
	detailed func(page int) (body, nextRow, nextID string)
	// detailedForRange, if set, takes priority over detailed and is consulted
	// with the request's start_date/end_date (YYYY-MM-DD) instead of a page
	// counter — lets a test supply different Toggl data per calendar year,
	// needed to distinguish a viewed period's data from a shifted
	// funding/spendable period's data that lands in a different year.
	detailedForRange func(startDate, endDate string) (body, nextRow, nextID string)
	projects         string // JSON for the v9 projects endpoint
	holidays         string // JSON for the OpenHolidays endpoint
	// failDetailed, when non-zero, makes every detailed POST return that status.
	failDetailed int
	// focus answers everything sent to focus.toggl.com (the Toggl 2.0 API);
	// nil means that host 404s.
	focus *fakeFocus
}

// fakeFocus routes Toggl 2.0 requests to canned responses and records every
// path it saw, so tests can assert on pagination and on what was refetched.
type fakeFocus struct {
	entries        func(page int) string // JSON array for the 1-based page; nil = []
	projects       string                // JSON array
	projectRates   map[int]string        // JSON array per project id
	workspaceRates string                // JSON array
	clients        string                // JSON array
	failEntries    int                   // non-zero: every time-entries GET returns this status
	settings       string                // JSON object for /users/me/settings
	context        string                // JSON object for /workspaces/{id}/context
	contextStatus  int                   // non-zero: the context GET returns this status
	calls          []string              // path?query of every request
}

func (f *fakeFocus) roundTrip(r *http.Request) (*http.Response, error) {
	f.calls = append(f.calls, r.URL.Path+"?"+r.URL.RawQuery)
	p := r.URL.Path
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	wrap := func(items string) *http.Response {
		if items == "" {
			items = "[]"
		}
		return jsonResponse(`{"data":`+items+`,"page":`+strconv.Itoa(page)+`,"per_page":200}`, nil)
	}
	switch {
	case strings.HasSuffix(p, "/users/me/settings"):
		if f.failEntries != 0 {
			return statusResponse(f.failEntries, `{"error":"unauthorized","error_description":"invalid api key"}`), nil
		}
		body := f.settings
		if body == "" {
			body = `{}`
		}
		return jsonResponse(body, nil), nil
	case strings.HasSuffix(p, "/context"):
		if f.contextStatus != 0 {
			return statusResponse(f.contextStatus, `{"error":"forbidden","error_description":"session authentication only"}`), nil
		}
		body := f.context
		if body == "" {
			body = `{}`
		}
		return jsonResponse(body, nil), nil
	case strings.HasSuffix(p, "/time-entries"):
		if f.failEntries != 0 {
			return statusResponse(f.failEntries, `{"error":"unauthorized","error_description":"invalid api key"}`), nil
		}
		if f.entries == nil {
			return wrap(""), nil
		}
		return wrap(f.entries(page)), nil
	case strings.Contains(p, "/billable-rates/projects/") && strings.HasSuffix(p, "/rates"):
		id, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(p[strings.Index(p, "/billable-rates/projects/"):], "/billable-rates/projects/"), "/rates"))
		body := f.projectRates[id]
		if body == "" {
			body = "[]"
		}
		return jsonResponse(body, nil), nil
	case strings.HasSuffix(p, "/billable-rates/workspace/rates"):
		body := f.workspaceRates
		if body == "" {
			body = "[]"
		}
		return jsonResponse(body, nil), nil
	case strings.HasSuffix(p, "/projects"):
		return wrap(f.projects), nil
	case strings.HasSuffix(p, "/clients"):
		return wrap(f.clients), nil
	}
	return statusResponse(http.StatusNotFound, "unexpected: "+r.URL.String()), nil
}

func (f *fakeFocus) callsTo(fragment string) int {
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, fragment) {
			n++
		}
	}
	return n
}

// transport returns an http.RoundTripper backed by b, and a pointer to a counter
// of detailed-report POSTs made.
func (b *fakeBackend) transport() *http.Client {
	page := 0
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Host == "focus.toggl.com":
			if b.focus == nil {
				return statusResponse(http.StatusNotFound, "unexpected: "+r.URL.String()), nil
			}
			return b.focus.roundTrip(r)
		case strings.Contains(r.URL.Path, "/search/time_entries"):
			if b.failDetailed != 0 {
				return statusResponse(b.failDetailed, `{"error":"boom"}`), nil
			}
			if b.detailedForRange != nil {
				var req struct {
					StartDate string `json:"start_date"`
					EndDate   string `json:"end_date"`
				}
				if r.Body != nil {
					_ = json.NewDecoder(r.Body).Decode(&req)
				}
				body, nextRow, nextID := b.detailedForRange(req.StartDate, req.EndDate)
				h := map[string]string{}
				if nextRow != "" {
					h["X-Next-Row-Number"] = nextRow
					h["X-Next-ID"] = nextID
				}
				return jsonResponse(body, h), nil
			}
			if b.detailed == nil {
				return jsonResponse(`[]`, nil), nil
			}
			body, nextRow, nextID := b.detailed(page)
			page++
			h := map[string]string{}
			if nextRow != "" {
				h["X-Next-Row-Number"] = nextRow
				h["X-Next-ID"] = nextID
			}
			return jsonResponse(body, h), nil
		case strings.Contains(r.URL.Path, "/projects"):
			body := b.projects
			if body == "" {
				body = `[]`
			}
			return jsonResponse(body, nil), nil
		case strings.Contains(r.URL.Host, "openholidays") || strings.Contains(r.URL.Path, "PublicHolidays"):
			body := b.holidays
			if body == "" {
				body = `[]`
			}
			return jsonResponse(body, nil), nil
		default:
			return statusResponse(http.StatusNotFound, "unexpected: "+r.URL.String()), nil
		}
	})
	return &http.Client{Transport: rt}
}
