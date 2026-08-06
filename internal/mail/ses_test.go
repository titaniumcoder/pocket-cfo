package mail

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// roundTripFunc lets a test assert whether an HTTP call was made at all.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSendLoginLink_NoFromLogsInsteadOfSending(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("no HTTP request should be made when From is unset")
		return nil, errors.New("unreachable")
	})}

	if err := SendLoginLink(context.Background(), client, Config{}, "person@example.com", "https://example.com/auth/email/callback?token=abc"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
