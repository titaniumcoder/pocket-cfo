package tracker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// countingTransport records every attempt and replays a scripted sequence of
// outcomes. The last entry repeats once the script runs out, so a test can
// script "fail, fail, then succeed" or "always fail" without counting exactly.
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

// togglWith builds a Toggl over rt. The retry backoff is already collapsed to
// a millisecond for the whole test binary (see TestMain), so these tests
// assert attempt counts rather than elapsed time — except TestDoHonoursRetryAfter,
// which needs a real wait and gets one from the header itself.
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
			// A 401 or 404 is a real answer, not a blip. Retrying it only
			// burns the request budget, so it comes straight back.
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

// TestDoResendsTheBodyOnRetry guards the thing that quietly breaks when a
// retry loop is bolted onto a POST: the first attempt consumes the reader, and
// every later one sends an empty body. The detailed report is a POST whose
// body carries the date range, so an empty retry would silently query nothing.
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

// TestDoHonoursRetryAfter checks the header is actually waited on: with
// Retry-After: 1 the call cannot return before roughly a second, whereas the
// default backoff for a first retry is 500ms.
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

// TestDoStopsRetryingWhenContextExpires: the retry loop must never outlive the
// caller's deadline. With a 50ms budget and a 30s Retry-After, the call has to
// give up almost immediately rather than sleeping through the request.
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
