package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	APIKey     string
	HTTPClient *http.Client
}

func (c *Client) endpoint() string {
	if strings.HasSuffix(c.APIKey, ":fx") {
		return "https://api-free.deepl.com/v2/translate"
	}
	return "https://api.deepl.com/v2/translate"
}

type translateResponse struct {
	Translations []struct {
		Text string `json:"text"`
	} `json:"translations"`
}

const maxRetries = 3

const statusQuotaExceeded = 456

func (c *Client) Translate(ctx context.Context, text, sourceLang, targetLang string) (string, error) {
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		out, retryable, err := c.attempt(ctx, text, sourceLang, targetLang)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !retryable {
			return "", err
		}
	}
	return "", fmt.Errorf("giving up after %d attempts: %w", maxRetries, lastErr)
}

func (c *Client) attempt(ctx context.Context, text, sourceLang, targetLang string) (out string, retryable bool, err error) {
	body := url.Values{}
	body.Set("text", text)
	body.Set("source_lang", strings.ToUpper(sourceLang))
	body.Set("target_lang", strings.ToUpper(targetLang))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), strings.NewReader(body.Encode()))
	if err != nil {
		return "", false, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "DeepL-Auth-Key "+c.APIKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", true, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", true, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		retryable := resp.StatusCode >= 500 ||
			resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == statusQuotaExceeded
		return "", retryable, fmt.Errorf("deepl rejected %d: %s", resp.StatusCode, truncate(respBody))
	}

	var parsed translateResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", false, fmt.Errorf("unexpected response: %s", truncate(respBody))
	}
	if len(parsed.Translations) == 0 {
		return "", false, fmt.Errorf("no translation in response: %s", truncate(respBody))
	}
	return parsed.Translations[0].Text, false, nil
}

func truncate(b []byte) string {
	const max = 500
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}
