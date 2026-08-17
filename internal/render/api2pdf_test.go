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

// TestAPI2PDF_Render_FileURLEnvelope is the shape api2pdf actually returns.
// It ignores outputBinary on this endpoint and answers with its usual JSON
// envelope carrying a FileUrl, so this — not the inline form below — is the
// path every real render takes.
//
// This test existed before v0.26.0 and was replaced by one that asserted the
// flag and handed back a fake %PDF- body. That mock agreed with the change
// rather than with the service, so CI passed and the first real render failed.
func TestAPI2PDF_Render_FileURLEnvelope(t *testing.T) {
	const fileURL = "https://files.example/out.pdf"
	var posted, downloaded bool
	var downloadAuth string

	renderer := &API2PDF{
		APIKey: "test-key",
		Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case api2pdfEndpoint:
				posted = true
				resp, _ := json.Marshal(api2pdfResponse{Success: true, FileUrl: fileURL})
				return newResponse(http.StatusOK, string(resp)), nil
			case fileURL:
				downloaded = true
				downloadAuth = req.Header.Get("Authorization")
				return newResponse(http.StatusOK, "%PDF-1.4 fake"), nil
			}
			t.Fatalf("unexpected request to %s", req.URL)
			return nil, nil
		})},
	}

	pdf, err := renderer.Render(context.Background(), []byte("<html/>"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !posted || !downloaded {
		t.Errorf("posted=%v downloaded=%v, want both — the envelope must be followed", posted, downloaded)
	}
	if !strings.HasPrefix(string(pdf), "%PDF-") {
		t.Errorf("Render returned non-PDF bytes: %q", pdf)
	}
	// The file-store URL is its own credential; sending the API key to it
	// would hand the key to whatever host api2pdf named.
	if downloadAuth != "" {
		t.Errorf("the download carried an Authorization header (%q)", downloadAuth)
	}
}

// An envelope with neither bytes nor a URL is a real failure and must not be
// mistaken for either shape.
func TestAPI2PDF_Render_EnvelopeWithNoFileURL(t *testing.T) {
	renderer := &API2PDF{
		APIKey: "test-key",
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			resp, _ := json.Marshal(api2pdfResponse{Success: true})
			return newResponse(http.StatusOK, string(resp)), nil
		})},
	}
	if _, err := renderer.Render(context.Background(), []byte("<html/>")); err == nil {
		t.Fatal("want an error when the response carries neither a PDF nor a URL")
	}
}

// The inline form, for the day api2pdf honours the flag. Asking for it costs
// nothing; requiring it is what broke.
func TestAPI2PDF_Render_Success(t *testing.T) {
	var gotAuth, gotContentType, gotMethod string
	var gotHTML string
	var gotOutputBinary bool

	renderer := &API2PDF{
		APIKey: "test-key",
		Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != api2pdfEndpoint {
				t.Fatalf("unexpected request to %s", req.URL)
				return nil, nil
			}
			gotMethod = req.Method
			gotAuth = req.Header.Get("Authorization")
			gotContentType = req.Header.Get("Content-Type")
			var decoded api2pdfRequest
			if err := json.NewDecoder(req.Body).Decode(&decoded); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			gotHTML = decoded.HTML
			gotOutputBinary = decoded.OutputBinary
			return newResponse(http.StatusOK, "%PDF-1.4 fake"), nil
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
	if !gotOutputBinary {
		t.Error("outputBinary was not requested, so the PDF goes through the 24-hour file store")
	}
}

func TestAPI2PDF_Render_JSONErrorInsteadOfBytes(t *testing.T) {
	renderer := &API2PDF{
		APIKey: "test-key",
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			resp, _ := json.Marshal(api2pdfResponse{Success: false, Error: "html too large"})
			return newResponse(http.StatusOK, string(resp)), nil
		})},
	}
	_, err := renderer.Render(context.Background(), []byte("<html/>"))
	if err == nil {
		t.Fatal("want an error when the response carries no PDF")
	}
	if !strings.Contains(err.Error(), "html too large") {
		t.Errorf("error = %q, want api2pdf's own reason", err)
	}
}

func TestAPI2PDF_Render_429IsRetried(t *testing.T) {
	attempts := 0
	renderer := &API2PDF{
		APIKey: "test-key",
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return newResponse(http.StatusTooManyRequests, "slow down"), nil
			}
			return newResponse(http.StatusOK, "%PDF-1.4 fake"), nil
		})},
	}
	pdf, err := renderer.Render(context.Background(), []byte("<html/>"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 — the throttle should have been retried", attempts)
	}
	if !strings.HasPrefix(string(pdf), "%PDF-") {
		t.Errorf("Render returned non-PDF bytes: %q", pdf)
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

func TestAPI2PDF_Balance_Success(t *testing.T) {
	var gotAuth, gotMethod, gotPath string
	renderer := &API2PDF{
		APIKey: "test-key",
		Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			gotAuth = req.Header.Get("Authorization")
			gotMethod = req.Method
			gotPath = req.URL.Path
			return newResponse(http.StatusOK, `{"Balance":12.5,"Currency":"USD"}`), nil
		})},
	}

	info, err := renderer.Balance(context.Background())
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if gotAuth != "test-key" {
		t.Errorf("Authorization header = %q, want test-key (no Bearer prefix)", gotAuth)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/balance" {
		t.Errorf("path = %q, want /balance", gotPath)
	}
	if !info.HasBalance || info.Balance != 12.5 {
		t.Errorf("Balance = %+v, want HasBalance=true Balance=12.5", info)
	}
	if info.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", info.Currency)
	}
	if info.Raw["Currency"] != "USD" {
		t.Errorf("Raw[Currency] = %q, want USD", info.Raw["Currency"])
	}
}

// TestAPI2PDF_Balance_UnexpectedShapeStillReturnsRaw is the defensive-parsing
// guarantee: api2pdf's real /balance field names aren't publicly documented
// (see BalanceInfo's doc comment), so a response with no recognizable
// "balance" field must still surface every field it does have, not error out
// entirely.
func TestAPI2PDF_Balance_UnexpectedShapeStillReturnsRaw(t *testing.T) {
	renderer := &API2PDF{
		APIKey: "test-key",
		Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return newResponse(http.StatusOK, `{"SomeOtherField":"mystery value"}`), nil
		})},
	}

	info, err := renderer.Balance(context.Background())
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if info.HasBalance {
		t.Error("HasBalance should be false when no balance-like field is present")
	}
	if info.Raw["SomeOtherField"] != "mystery value" {
		t.Errorf("Raw[SomeOtherField] = %q, want it preserved for fallback display", info.Raw["SomeOtherField"])
	}
}

// TestAPI2PDF_Balance_FormatsEveryNumericRawField is the practical case:
// api2pdf's real field names aren't documented, so the key that actually
// carries the money is quite likely one we don't recognize as "the"
// balance. Whatever it's called, it must not surface as a bare Go float
// next to properly formatted figures.
func TestAPI2PDF_Balance_FormatsEveryNumericRawField(t *testing.T) {
	renderer := &API2PDF{
		APIKey: "test-key",
		Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return newResponse(http.StatusOK, `{"remainingCredits":1234.5,"used":0.75,"plan":"pro"}`), nil
		})},
	}

	info, err := renderer.Balance(context.Background())
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	nbsp := " "
	if want := "1" + nbsp + "234,50"; info.Raw["remainingCredits"] != want {
		t.Errorf("Raw[remainingCredits] = %q, want %q", info.Raw["remainingCredits"], want)
	}
	if want := "0,75"; info.Raw["used"] != want {
		t.Errorf("Raw[used] = %q, want %q", info.Raw["used"], want)
	}
	if info.Raw["plan"] != "pro" {
		t.Errorf("Raw[plan] = %q, want the string left alone", info.Raw["plan"])
	}
}

func TestAPI2PDF_Balance_EmptyAPIKey(t *testing.T) {
	renderer := &API2PDF{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("must not make a request with an empty API key")
		return nil, nil
	})}}
	if _, err := renderer.Balance(context.Background()); err == nil {
		t.Fatal("expected an error with an empty API key")
	}
}

func TestAPI2PDF_Balance_ErrorStatus(t *testing.T) {
	renderer := &API2PDF{
		APIKey: "test-key",
		Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return newResponse(http.StatusUnauthorized, `{"Error":"invalid key"}`), nil
		})},
	}
	if _, err := renderer.Balance(context.Background()); err == nil {
		t.Fatal("expected an error on 401")
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
