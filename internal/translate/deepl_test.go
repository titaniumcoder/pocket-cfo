package translate

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type fakeTransport struct {
	t       *testing.T
	handler func(*testing.T, *http.Request) *http.Response
}

func (f fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f.handler(f.t, req), nil
}

func TestClient_Translate(t *testing.T) {
	client := &Client{
		APIKey: "test-key:fx",
		HTTPClient: &http.Client{Transport: fakeTransport{t: t, handler: func(t *testing.T, req *http.Request) *http.Response {
			want := "https://api-free.deepl.com/v2/translate"
			if req.URL.String() != want {
				t.Fatalf("unexpected URL: %s, want %s (free-tier key should hit the free endpoint)", req.URL, want)
			}
			if req.Header.Get("Authorization") != "DeepL-Auth-Key test-key:fx" {
				t.Fatalf("unexpected Authorization header: %s", req.Header.Get("Authorization"))
			}
			body, _ := io.ReadAll(req.Body)
			form, _ := url.ParseQuery(string(body))
			if form.Get("text") != "Hallo" || form.Get("source_lang") != "DE" || form.Get("target_lang") != "BG" {
				t.Fatalf("unexpected form body: %v", form)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"translations":[{"text":"Здравей"}]}`)),
			}
		}}},
	}

	got, err := client.Translate(context.Background(), "Hallo", "de", "bg")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got != "Здравей" {
		t.Errorf("got %q, want %q", got, "Здравей")
	}
}

func TestClient_Translate_PaidEndpoint(t *testing.T) {
	client := &Client{
		APIKey: "test-key", // no ":fx" suffix — paid tier
		HTTPClient: &http.Client{Transport: fakeTransport{t: t, handler: func(t *testing.T, req *http.Request) *http.Response {
			want := "https://api.deepl.com/v2/translate"
			if req.URL.String() != want {
				t.Fatalf("unexpected URL: %s, want %s (paid key should hit the paid endpoint)", req.URL, want)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"translations":[{"text":"ok"}]}`)),
			}
		}}},
	}
	if _, err := client.Translate(context.Background(), "x", "de", "bg"); err != nil {
		t.Fatalf("Translate: %v", err)
	}
}

func TestClient_Translate_Error(t *testing.T) {
	client := &Client{
		APIKey: "test-key:fx",
		HTTPClient: &http.Client{Transport: fakeTransport{t: t, handler: func(t *testing.T, req *http.Request) *http.Response {
			return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(`{"message":"quota exceeded"}`))}
		}}},
	}
	if _, err := client.Translate(context.Background(), "x", "de", "bg"); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestClient_Translate_RetriesTransientFailures(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, statusQuotaExceeded, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			attempts := 0
			client := &Client{
				APIKey: "test-key:fx",
				HTTPClient: &http.Client{Transport: fakeTransport{t: t, handler: func(t *testing.T, req *http.Request) *http.Response {
					attempts++
					if attempts == 1 {
						return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"message":"later"}`))}
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"translations":[{"text":"Здравей"}]}`))}
				}}},
			}
			got, err := client.Translate(context.Background(), "Hallo", "de", "bg")
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			if got != "Здравей" {
				t.Errorf("Translate = %q, want %q", got, "Здравей")
			}
			if attempts != 2 {
				t.Errorf("attempts = %d, want 2", attempts)
			}
		})
	}
}

func TestClient_Translate_DoesNotRetryARealRejection(t *testing.T) {
	attempts := 0
	client := &Client{
		APIKey: "test-key:fx",
		HTTPClient: &http.Client{Transport: fakeTransport{t: t, handler: func(t *testing.T, req *http.Request) *http.Response {
			attempts++
			return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(`{"message":"bad key"}`))}
		}}},
	}
	if _, err := client.Translate(context.Background(), "x", "de", "bg"); err == nil {
		t.Fatal("expected an error, got nil")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 — a wrong API key is not going to fix itself", attempts)
	}
}
