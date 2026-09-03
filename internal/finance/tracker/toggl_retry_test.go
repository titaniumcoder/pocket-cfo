package tracker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// countingTransport replays a scripted sequence of outcomes, repeating the last
// entry once the script runs out.
type countingTransport struct {
	calls    int
	bodies   []string
	outcomes []func() (*http.Response, error)
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.calls++
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		c.bodies = append(c.bodies, string(b))
	}
	i := c.calls - 1
	if i >= len(c.outcomes) {
		i = len(c.outcomes) - 1
	}
	return c.outcomes[i]()
}

func ok200() (*http.Response, error) { return jsonResponse(`[]`, nil), nil }

func status(code int, headers map[string]string) func() (*http.Response, error) {
	return func() (*http.Response, error) {
		resp := statusResponse(code, `{"error":"boom"}`)
		for k, v := range headers {
			resp.Header.Set(k, v)
		}
		return resp, nil
	}
}

func transportError() (*http.Response, error) {
	return nil, errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)")
}

// togglWith builds a Toggl over rt. Backoff is collapsed binary-wide by
// TestMain, so these assert attempt counts rather than elapsed time.
func togglWith(rt http.RoundTripper) *Toggl {
	return &Toggl{Token: "tok", WorkspaceID: "ws", HTTP: &http.Client{Transport: rt}}
}

func TestDoRetriesTransientFailures(t *testing.T) {
	tests := []struct {
		name      string
		outcomes  []func() (*http.Response, error)
		wantCalls int
		wantErr   bool
	}{
		{
			name:      "succeeds first time",
			outcomes:  []func() (*http.Response, error){ok200},
			wantCalls: 1,
		},
		{
			name:      "timeout then success",
			outcomes:  []func() (*http.Response, error){transportError, ok200},
			wantCalls: 2,
		},
		{
			name:      "429 then success",
			outcomes:  []func() (*http.Response, error){status(http.StatusTooManyRequests, nil), ok200},
			wantCalls: 2,
		},
		{
			name:      "500 then success",
			outcomes:  []func() (*http.Response, error){status(http.StatusInternalServerError, nil), ok200},
			wantCalls: 2,
		},
		{
			name:      "gives up after togglAttempts",
			outcomes:  []func() (*http.Response, error){status(http.StatusBadGateway, nil)},
			wantCalls: togglAttempts,
			wantErr:   true,
		},
		{
			// A real answer, not a blip — retrying only burns the budget.
			name:      "401 is not retried",
			outcomes:  []func() (*http.Response, error){status(http.StatusUnauthorized, nil)},
			wantCalls: 1,
		},
		{
			name:      "404 is not retried",
			outcomes:  []func() (*http.Response, error){status(http.StatusNotFound, nil)},
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &countingTransport{outcomes: tt.outcomes}
			resp, err := togglWith(rt).do(context.Background(), http.MethodGet, "https://example.test/x", nil)
			if resp != nil {
				resp.Body.Close()
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if rt.calls != tt.wantCalls {
				t.Errorf("made %d attempts, want %d", rt.calls, tt.wantCalls)
			}
		})
	}
}

// The first attempt consumes the reader, so without buffering a retry posts an
// empty body — and the detailed report's body carries the date range.
func TestDoResendsTheBodyOnRetry(t *testing.T) {
	rt := &countingTransport{outcomes: []func() (*http.Response, error){
		status(http.StatusInternalServerError, nil),
		ok200,
	}}
	body := strings.NewReader(`{"start_date":"2026-03-01"}`)
	resp, err := togglWith(rt).do(context.Background(), http.MethodPost, "https://example.test/x", body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if len(rt.bodies) != 2 {
		t.Fatalf("recorded %d bodies, want 2", len(rt.bodies))
	}
	if rt.bodies[0] != rt.bodies[1] {
		t.Errorf("retry sent %q, want the original %q", rt.bodies[1], rt.bodies[0])
	}
}

// Retry-After: 1 must hold the call for ~1s, well past the default backoff.
func TestDoHonoursRetryAfter(t *testing.T) {
	rt := &countingTransport{outcomes: []func() (*http.Response, error){
		status(http.StatusTooManyRequests, map[string]string{"Retry-After": "1"}),
		ok200,
	}}
	start := time.Now()
	resp, err := togglWith(rt).do(context.Background(), http.MethodGet, "https://example.test/x", nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if elapsed < 900*time.Millisecond {
		t.Errorf("returned after %s, want at least ~1s (Retry-After was ignored)", elapsed)
	}
	if rt.calls != 2 {
		t.Errorf("made %d attempts, want 2", rt.calls)
	}
}

// The retry loop must never outlive the caller's deadline: a 50ms budget
// against a 30s Retry-After has to give up at once.
func TestDoStopsRetryingWhenContextExpires(t *testing.T) {
	rt := &countingTransport{outcomes: []func() (*http.Response, error){
		status(http.StatusTooManyRequests, map[string]string{"Retry-After": "30"}),
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := togglWith(rt).do(ctx, http.MethodGet, "https://example.test/x", nil); err == nil {
		t.Fatal("expected an error once the deadline passes")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s — the retry slept past the caller's deadline", elapsed)
	}
	if rt.calls != 1 {
		t.Errorf("made %d attempts, want 1 (no budget for a second)", rt.calls)
	}
}

func TestAPIErrorCarriesItsStatus(t *testing.T) {
	err := apiError("toggl", statusResponse(http.StatusUnauthorized, "unauthorized"))

	if got, want := err.Error(), "toggl: status 401: unauthorized"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !isUnauthorized(err) {
		t.Error("a 401 is not recognised as unauthorized")
	}
	if isUnauthorized(fmt.Errorf("funding: %w", apiError("toggl", statusResponse(http.StatusNotFound, "nope")))) {
		t.Error("a 404 counts as unauthorized")
	}
	if !isUnauthorized(fmt.Errorf("funding: toggl 2026: %w", err)) {
		t.Error("wrapping hides the status")
	}
}
