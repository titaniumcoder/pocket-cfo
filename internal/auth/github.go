package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// scope is the minimum GitHub OAuth scope needed: read the user's identity
// and check their permission on the private repo. See ARCHITECTURE.md §8.
const scope = "read:user repo"

// AuthorizeURL builds the GitHub OAuth authorize link to redirect the user
// to. state is an opaque CSRF token the caller must also stash (e.g. in a
// short-lived cookie) and compare on callback.
func AuthorizeURL(clientID, redirectURI, state string) string {
	v := url.Values{}
	v.Set("client_id", clientID)
	v.Set("redirect_uri", redirectURI)
	v.Set("scope", scope)
	v.Set("state", state)
	return "https://github.com/login/oauth/authorize?" + v.Encode()
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// ExchangeCode trades the OAuth callback's code for an access token.
func ExchangeCode(ctx context.Context, client *http.Client, clientID, clientSecret, code string) (string, error) {
	body := url.Values{}
	body.Set("client_id", clientID)
	body.Set("client_secret", clientSecret)
	body.Set("code", code)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(body.Encode()))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token exchange rejected %d: %s", resp.StatusCode, truncate(respBody))
	}

	var parsed tokenResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("unexpected response: %s", truncate(respBody))
	}
	if parsed.Error != "" {
		return "", fmt.Errorf("github: %s: %s", parsed.Error, parsed.ErrorDescription)
	}
	if parsed.AccessToken == "" {
		return "", fmt.Errorf("no access_token in response: %s", truncate(respBody))
	}
	return parsed.AccessToken, nil
}

type userResponse struct {
	Login string `json:"login"`
}

// CurrentUser returns the login of the account accessToken belongs to.
func CurrentUser(ctx context.Context, client *http.Client, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	setAuthHeaders(req, accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET /user rejected %d: %s", resp.StatusCode, truncate(respBody))
	}

	var parsed userResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("unexpected response: %s", truncate(respBody))
	}
	if parsed.Login == "" {
		return "", fmt.Errorf("no login in response: %s", truncate(respBody))
	}
	return parsed.Login, nil
}

type permissionResponse struct {
	Permission string `json:"permission"`
}

// CollaboratorPermission checks login's permission level on repo
// ("owner/name"), per ARCHITECTURE.md §8:
// GET /repos/{repo}/collaborators/{login}/permission. Returns "" (not an
// error) if login isn't a collaborator at all — the 404 case — so callers
// can treat "not push/admin" uniformly regardless of which of those it was.
func CollaboratorPermission(ctx context.Context, client *http.Client, accessToken, repo, login string) (string, error) {
	u := fmt.Sprintf("https://api.github.com/repos/%s/collaborators/%s/permission", repo, login)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	setAuthHeaders(req, accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("permission check rejected %d: %s", resp.StatusCode, truncate(respBody))
	}

	var parsed permissionResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("unexpected response: %s", truncate(respBody))
	}
	return parsed.Permission, nil
}

func setAuthHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
}

func truncate(b []byte) string {
	const max = 500
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}
