package auth

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// fakeTransport routes requests to a handler by exact URL, so tests can
// assert what was requested (method, headers, body) without a real network
// call, and control exactly what comes back.
type fakeTransport struct {
	t       *testing.T
	handler func(*testing.T, *http.Request) *http.Response
}

func (f fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f.handler(f.t, req), nil
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func TestAuthorizeURL(t *testing.T) {
	got, err := url.Parse(AuthorizeURL("client-id", "https://example.com/auth/callback", "state-123"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "github.com" || got.Path != "/login/oauth/authorize" {
		t.Fatalf("unexpected host/path: %s", got)
	}
	q := got.Query()
	if q.Get("client_id") != "client-id" {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != "https://example.com/auth/callback" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	if q.Get("state") != "state-123" {
		t.Errorf("state = %q", q.Get("state"))
	}
	if q.Get("scope") != "read:user repo" {
		t.Errorf("scope = %q", q.Get("scope"))
	}
}

func TestExchangeCode(t *testing.T) {
	client := &http.Client{Transport: fakeTransport{t: t, handler: func(t *testing.T, req *http.Request) *http.Response {
		if req.Method != http.MethodPost || req.URL.String() != "https://github.com/login/oauth/access_token" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
		}
		body, _ := io.ReadAll(req.Body)
		form, _ := url.ParseQuery(string(body))
		if form.Get("client_id") != "cid" || form.Get("client_secret") != "csecret" || form.Get("code") != "the-code" {
			t.Fatalf("unexpected form body: %v", form)
		}
		return jsonResponse(http.StatusOK, `{"access_token":"tok-abc","token_type":"bearer"}`)
	}}}

	got, err := ExchangeCode(context.Background(), client, "cid", "csecret", "the-code")
	if err != nil {
		t.Fatal(err)
	}
	if got != "tok-abc" {
		t.Errorf("got %q, want tok-abc", got)
	}
}

func TestExchangeCode_GitHubError(t *testing.T) {
	client := &http.Client{Transport: fakeTransport{t: t, handler: func(t *testing.T, req *http.Request) *http.Response {
		return jsonResponse(http.StatusOK, `{"error":"bad_verification_code","error_description":"nope"}`)
	}}}
	if _, err := ExchangeCode(context.Background(), client, "cid", "csecret", "bad-code"); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestCurrentUser(t *testing.T) {
	client := &http.Client{Transport: fakeTransport{t: t, handler: func(t *testing.T, req *http.Request) *http.Response {
		if req.URL.String() != "https://api.github.com/user" {
			t.Fatalf("unexpected URL: %s", req.URL)
		}
		if req.Header.Get("Authorization") != "Bearer tok-abc" {
			t.Fatalf("unexpected Authorization header: %s", req.Header.Get("Authorization"))
		}
		return jsonResponse(http.StatusOK, `{"login":"octocat"}`)
	}}}

	got, err := CurrentUser(context.Background(), client, "tok-abc")
	if err != nil {
		t.Fatal(err)
	}
	if got != "octocat" {
		t.Errorf("got %q, want octocat", got)
	}
}

func TestCollaboratorPermission(t *testing.T) {
	client := &http.Client{Transport: fakeTransport{t: t, handler: func(t *testing.T, req *http.Request) *http.Response {
		want := "https://api.github.com/repos/titaniumcoder/pocket-cfo/collaborators/octocat/permission"
		if req.URL.String() != want {
			t.Fatalf("unexpected URL: %s, want %s", req.URL, want)
		}
		return jsonResponse(http.StatusOK, `{"permission":"admin"}`)
	}}}

	got, err := CollaboratorPermission(context.Background(), client, "tok-abc", "titaniumcoder/pocket-cfo", "octocat")
	if err != nil {
		t.Fatal(err)
	}
	if got != "admin" {
		t.Errorf("got %q, want admin", got)
	}
}

func TestCollaboratorPermission_NotACollaborator(t *testing.T) {
	client := &http.Client{Transport: fakeTransport{t: t, handler: func(t *testing.T, req *http.Request) *http.Response {
		return jsonResponse(http.StatusNotFound, `{"message":"Not Found"}`)
	}}}

	got, err := CollaboratorPermission(context.Background(), client, "tok-abc", "titaniumcoder/pocket-cfo", "stranger")
	if err != nil {
		t.Fatalf("404 should not be an error, got: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty string for a non-collaborator", got)
	}
}
