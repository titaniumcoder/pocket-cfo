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

var ErrNotFound = errors.New("not found")

type Store interface {
	Get(ctx context.Context, path string) (content []byte, sha string, err error)
	Put(ctx context.Context, path string, content []byte, baseSHA, message string) (sha string, err error)
	List(ctx context.Context, path string) (names []string, err error)
}

type ContentsClient struct {
	HTTP    *http.Client
	Repo    string
	Token   string
	BaseURL string
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
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(body.Content, "\n", ""))
	if err != nil {
		return nil, "", fmt.Errorf("github GET %s: decoding content: %w", path, err)
	}
	return decoded, body.SHA, nil
}

type contentsEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func (c *ContentsClient) List(ctx context.Context, path string) ([]string, error) {
	url := fmt.Sprintf("%s/repos/%s/contents/%s", c.baseURL(), c.Repo, path)
	resp, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return []string{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github GET %s: %s", path, resp.Status)
	}

	var entries []contentsEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("github GET %s: %w", path, err)
	}
	var out []string
	for _, e := range entries {
		if e.Type == "file" {
			out = append(out, e.Name)
		}
	}
	return out, nil
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
	case http.StatusUnprocessableEntity:
		return "", errorf(CodeUpstream,
			"GitHub refused the write to %s (422) — this is not a conflict, so re-reading and retrying will not help. A month file that has grown past the Contents API limit is the usual cause.", path)
	case http.StatusConflict:
		return "", &Error{Code: CodeConflict, Message: fmt.Sprintf("%s changed underneath us; re-read and merge", path)}
	}
	return "", fmt.Errorf("github PUT %s: %s", path, resp.Status)
}
