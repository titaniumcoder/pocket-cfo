package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ErrNotFound means the path doesn't exist at that ref — a month never
// committed, which is a normal state rather than a failure.
var ErrNotFound = errors.New("not found")

// Store is the git side of a write. The interface exists so the pipeline is
// testable without a fake GitHub, and so it can never accidentally reach a
// filesystem: the app must not write its own DATA_DIR, which is an ephemeral
// mounted checkout whose changes would be lost on restart and diverge from
// git.
type Store interface {
	Get(ctx context.Context, path string) (content []byte, sha string, err error)
	Put(ctx context.Context, path string, content []byte, baseSHA, message string) (sha string, err error)
}

// ContentsClient writes through the GitHub Contents API, so every accepted
// write is a commit that can be read and reverted.
type ContentsClient struct {
	HTTP    *http.Client
	Repo    string // "owner/name"
	Token   string
	BaseURL string // defaults to https://api.github.com; the httptest seam
}

func (c *ContentsClient) baseURL() string {
	if c.BaseURL != "" {
		return strings.TrimSuffix(c.BaseURL, "/")
	}
	return "https://api.github.com"
}

func (c *ContentsClient) do(ctx context.Context, method, url string, body []byte) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}

type contentsResponse struct {
	Content string `json:"content"`
	SHA     string `json:"sha"`
}

// Get returns the file's decoded content and its blob sha.
func (c *ContentsClient) Get(ctx context.Context, path string) ([]byte, string, error) {
	url := fmt.Sprintf("%s/repos/%s/contents/%s", c.baseURL(), c.Repo, path)
	resp, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, "", ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("github GET %s: %s", path, resp.Status)
	}

	var body contentsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, "", fmt.Errorf("github GET %s: %w", path, err)
	}
	// GitHub wraps base64 at 60 columns.
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(body.Content, "\n", ""))
	if err != nil {
		return nil, "", fmt.Errorf("github GET %s: decoding content: %w", path, err)
	}
	return decoded, body.SHA, nil
}

type putRequest struct {
	Message string `json:"message"`
	Content string `json:"content"`
	SHA     string `json:"sha,omitempty"`
}

type putResponse struct {
	Content struct {
		SHA string `json:"sha"`
	} `json:"content"`
}

// Put commits content at path. An empty baseSHA creates the file; a mismatch
// is GitHub's own 409, which the caller maps to a conflict so Hermes re-reads
// rather than clobbering.
func (c *ContentsClient) Put(ctx context.Context, path string, content []byte, baseSHA, message string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/contents/%s", c.baseURL(), c.Repo, path)
	body, err := json.Marshal(putRequest{
		Message: message,
		Content: base64.StdEncoding.EncodeToString(content),
		SHA:     baseSHA,
	})
	if err != nil {
		return "", err
	}

	resp, err := c.do(ctx, http.MethodPut, url, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		var out putResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return "", fmt.Errorf("github PUT %s: %w", path, err)
		}
		return out.Content.SHA, nil
	case http.StatusConflict, http.StatusUnprocessableEntity:
		return "", &Error{Code: CodeConflict, Message: fmt.Sprintf("%s changed underneath us; re-read and merge", path)}
	}
	return "", fmt.Errorf("github PUT %s: %s", path, resp.Status)
}
