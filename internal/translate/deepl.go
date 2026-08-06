// Package translate fills in missing Bulgarian text on draft invoices via
// DeepL. It is deliberately narrow: ordinary content only (line
// descriptions, discount labels) — never the tax note, which is
// catalog-sourced and human-authored (see catalog/notes.json), and never an
// issued invoice, which is immutable once sent. See cmd/invoicectl/translate.go
// for the command that uses this.
package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Client calls the DeepL API. Free-tier keys (suffixed ":fx", DeepL's own
// convention) use the free endpoint; anything else uses the paid one.
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

// Translate returns text translated from sourceLang to targetLang (DeepL
// language codes, e.g. "DE", "EN", "FR", "BG" — case-insensitive on the
// wire, but DeepL's own docs use uppercase).
func (c *Client) Translate(ctx context.Context, text, sourceLang, targetLang string) (string, error) {
	body := url.Values{}
	body.Set("text", text)
	body.Set("source_lang", strings.ToUpper(sourceLang))
	body.Set("target_lang", strings.ToUpper(targetLang))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), strings.NewReader(body.Encode()))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "DeepL-Auth-Key "+c.APIKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("deepl rejected %d: %s", resp.StatusCode, truncate(respBody))
	}

	var parsed translateResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("unexpected response: %s", truncate(respBody))
	}
	if len(parsed.Translations) == 0 {
		return "", fmt.Errorf("no translation in response: %s", truncate(respBody))
	}
	return parsed.Translations[0].Text, nil
}

func truncate(b []byte) string {
	const max = 500
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}
