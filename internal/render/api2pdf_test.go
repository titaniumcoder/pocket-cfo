package render

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// roundTripFunc adapts a function to http.RoundTripper, so API2PDF's real
// *http.Client can be pointed at an in-process fake without a real network
// call — api2pdfEndpoint is a hardcoded constant, so this is the only way
// to intercept the request.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func newResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestAPI2PDF_Render_Success(t *testing.T) {
	var gotAuth, gotContentType, gotMethod string
	var gotHTML string

	renderer := &API2PDF{
		APIKey: "test-key",
		Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case api2pdfEndpoint:
				gotMethod = req.Method
				gotAuth = req.Header.Get("Authorization")
				gotContentType = req.Header.Get("Content-Type")
				var decoded api2pdfRequest
				if err := json.NewDecoder(req.Body).Decode(&decoded); err != nil {
					t.Fatalf("decode request body: %v", err)
				}
				gotHTML = decoded.HTML
				resp, _ := json.Marshal(api2pdfResponse{Success: true, FileUrl: "https://files.example/out.pdf"})
				return newResponse(http.StatusOK, string(resp)), nil
			case "https://files.example/out.pdf":
				return newResponse(http.StatusOK, "%PDF-1.4 fake"), nil
			default:
				t.Fatalf("unexpected request to %s", req.URL)
				return nil, nil
			}
		})},
	}

	pdf, err := renderer.Render(context.Background(), []byte("<html>hi</html>"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.HasPrefix(string(pdf), "%PDF-") {
		t.Errorf("Render returned non-PDF bytes: %q", pdf)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotAuth != "test-key" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "test-key")
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotHTML != "<html>hi</html>" {
		t.Errorf("request HTML = %q, want the input unchanged", gotHTML)
	}
}

func TestAPI2PDF_Render_EmptyAPIKey(t *testing.T) {
	renderer := &API2PDF{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("must not make a request with an empty API key")
		return nil, nil
	})}}
	if _, err := renderer.Render(context.Background(), []byte("<html/>")); err == nil {
		t.Fatal("expected an error with an empty API key")
	}
}

func TestAPI2PDF_Render_4xxNotRetried(t *testing.T) {
	calls := 0
	renderer := &API2PDF{
		APIKey: "test-key",
		Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return newResponse(http.StatusBadRequest, `{"Success":false,"Error":"bad html"}`), nil
		})},
	}
	if _, err := renderer.Render(context.Background(), []byte("<html/>")); err == nil {
		t.Fatal("expected an error on 4xx")
	}
	if calls != 1 {
		t.Errorf("got %d requests, want exactly 1 (4xx must not retry)", calls)
	}
}

func TestAPI2PDF_Render_5xxRetriesThenGivesUp(t *testing.T) {
	calls := 0
	renderer := &API2PDF{
		APIKey: "test-key",
		Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return newResponse(http.StatusServiceUnavailable, "down for maintenance"), nil
		})},
	}

	// A short deadline stands in for waiting out the real backoff between
	// retries — ctx.Done() fires before time.After(backoff) either way,
	// so this exercises the retry/give-up path without a slow test.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := renderer.Render(ctx, []byte("<html/>"))
	if err == nil {
		t.Fatal("expected an error when every attempt gets a 5xx")
	}
	if !errors.Is(err, context.DeadlineExceeded) && calls < 1 {
		t.Errorf("expected at least one request before giving up, got %d", calls)
	}
}
