package mail

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
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

// sesSendEmailSuccessXML is the AWS SES Query-protocol (XML) response
// SendLoginLink's SDK client expects on a successful SendEmail call.
const sesSendEmailSuccessXML = `<SendEmailResponse xmlns="http://ses.amazonaws.com/doc/2010-12-01/">
  <SendEmailResult>
    <MessageId>test-message-id</MessageId>
  </SendEmailResult>
  <ResponseMetadata>
    <RequestId>test-request-id</RequestId>
  </ResponseMetadata>
</SendEmailResponse>`

func TestSendLoginLink_WithFromSendsViaSES(t *testing.T) {
	// Static credentials so the AWS SDK's default chain resolves instantly,
	// with no real network/IMDS lookup, in a sandboxed test environment.
	t.Setenv("AWS_ACCESS_KEY_ID", "test-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret")
	t.Setenv("AWS_SESSION_TOKEN", "")

	var capturedBody string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		capturedBody = string(b)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/xml"}},
			Body:       io.NopCloser(strings.NewReader(sesSendEmailSuccessXML)),
		}, nil
	})}

	cfg := Config{Region: "eu-west-1", From: "sender@example.com"}
	if err := SendLoginLink(context.Background(), client, cfg, "person@example.com", "https://example.com/link"); err != nil {
		t.Fatalf("unexpected error: %v (request body: %s)", err, capturedBody)
	}
	if !strings.Contains(capturedBody, "person%40example.com") {
		t.Errorf("request body missing the recipient: %s", capturedBody)
	}
}

func TestSendLoginLink_WithFromWrapsSESError(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret")
	t.Setenv("AWS_SESSION_TOKEN", "")

	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("simulated network failure")
	})}

	cfg := Config{Region: "eu-west-1", From: "sender@example.com"}
	err := SendLoginLink(context.Background(), client, cfg, "person@example.com", "https://example.com/link")
	if err == nil {
		t.Fatal("want an error when the underlying SES request fails")
	}
	if !strings.Contains(err.Error(), "ses SendEmail") {
		t.Errorf("error = %v, want it wrapped with the ses SendEmail context", err)
	}
}
