package render

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

type Renderer interface {
	Render(ctx context.Context, html []byte) ([]byte, error)
}

const api2pdfEndpoint = "https://v2.api2pdf.com/chrome/pdf/html"
const api2pdfBalanceEndpoint = "https://v2.api2pdf.com/balance"

const fontDelay = 1500 * time.Millisecond

const maxRetries = 3

type API2PDF struct {
	APIKey string
	Client *http.Client
}

func NewAPI2PDF(apiKey string) *API2PDF {
	return &API2PDF{
		APIKey: apiKey,
		Client: &http.Client{Timeout: 60 * time.Second},
	}
}

type api2pdfOptions struct {
	Delay int64 `json:"delay"`
}

type api2pdfRequest struct {
	HTML         string         `json:"html"`
	OutputBinary bool           `json:"outputBinary"`
	Options      api2pdfOptions `json:"options"`
}

type api2pdfResponse struct {
	Success bool   `json:"Success"`
	Error   string `json:"Error"`
	FileUrl string `json:"FileUrl"`
}

func (r *API2PDF) Render(ctx context.Context, html []byte) ([]byte, error) {
	if r.APIKey == "" {
		return nil, fmt.Errorf("api2pdf: API key is empty")
	}

	body, err := json.Marshal(api2pdfRequest{
		HTML:         string(html),
		OutputBinary: true,
		Options:      api2pdfOptions{Delay: fontDelay.Milliseconds()},
	})
	if err != nil {
		return nil, fmt.Errorf("api2pdf: encode request: %w", err)
	}

	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			backoff := time.Duration(attempt) * 2 * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		pdf, retryable, err := r.attempt(ctx, body)
		if err == nil {
			return pdf, nil
		}
		lastErr = err
		if !retryable {
			return nil, err
		}
	}
	return nil, fmt.Errorf("api2pdf: giving up after %d attempts: %w", maxRetries, lastErr)
}

type BalanceInfo struct {
	Balance    float64
	HasBalance bool
	Currency   string
	Raw        map[string]string
}

func (r *API2PDF) Balance(ctx context.Context) (BalanceInfo, error) {
	if r.APIKey == "" {
		return BalanceInfo{}, fmt.Errorf("api2pdf: API key is empty")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api2pdfBalanceEndpoint, nil)
	if err != nil {
		return BalanceInfo{}, fmt.Errorf("api2pdf: build balance request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", r.APIKey)

	resp, err := r.Client.Do(req)
	if err != nil {
		return BalanceInfo{}, fmt.Errorf("api2pdf: balance request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return BalanceInfo{}, fmt.Errorf("api2pdf: read balance response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return BalanceInfo{}, fmt.Errorf("api2pdf: balance status %d: %s", resp.StatusCode, truncate(body, 500))
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return BalanceInfo{}, fmt.Errorf("api2pdf: unexpected balance response: %s", truncate(body, 500))
	}

	info := BalanceInfo{Raw: make(map[string]string, len(raw))}
	for k, v := range raw {
		if f, ok := v.(float64); ok {
			info.Raw[k] = FormatAmount(int64(math.Round(f * 100)))
		} else {
			info.Raw[k] = fmt.Sprintf("%v", v)
		}
		if !info.HasBalance && strings.EqualFold(k, "balance") {
			if f, ok := v.(float64); ok {
				info.Balance = f
				info.HasBalance = true
			}
		}
		if info.Currency == "" && strings.EqualFold(k, "currency") {
			if s, ok := v.(string); ok {
				info.Currency = s
			}
		}
	}
	return info, nil
}

func (r *API2PDF) attempt(ctx context.Context, body []byte) (pdf []byte, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api2pdfEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("api2pdf: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", r.APIKey)

	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, true, fmt.Errorf("api2pdf: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, fmt.Errorf("api2pdf: read response: %w", err)
	}

	if resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("api2pdf: server error %d: %s", resp.StatusCode, truncate(respBody, 500))
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, true, fmt.Errorf("api2pdf: throttled: %s", truncate(respBody, 500))
	}
	if resp.StatusCode >= 400 {
		return nil, false, fmt.Errorf("api2pdf: request rejected %d: %s", resp.StatusCode, truncate(respBody, 500))
	}

	// outputBinary is asked for but not relied on. api2pdf ignores it on this
	// endpoint and answers with its usual JSON envelope, so both shapes have to
	// work: the bytes inline if we ever get them, and the file store if not.
	// Requiring the inline form broke every render on first contact with the
	// real service.
	if bytes.HasPrefix(respBody, []byte("%PDF-")) {
		return respBody, false, nil
	}

	var parsed api2pdfResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, false, fmt.Errorf("api2pdf: response is neither a PDF nor JSON: %s", truncate(respBody, 500))
	}
	if parsed.Error != "" {
		return nil, false, fmt.Errorf("api2pdf: %s", parsed.Error)
	}
	if parsed.FileUrl == "" {
		return nil, false, fmt.Errorf("api2pdf: no PDF and no file URL in the response: %s", truncate(respBody, 500))
	}
	return r.fetch(ctx, parsed.FileUrl)
}

// fetch downloads from the file store api2pdf puts the document in when it
// does not hand the bytes back. The URL needs no credentials, which is the
// reason outputBinary is asked for at all — the document sits there for 24
// hours behind a link that is its own bearer token. Until api2pdf honours the
// flag this is the only way to collect the PDF, so the prefix check below is
// what stops anything that is not one being written into the repo.
func (r *API2PDF) fetch(ctx context.Context, url string) (pdf []byte, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, fmt.Errorf("api2pdf: build download request: %w", err)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, true, fmt.Errorf("api2pdf: download failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, fmt.Errorf("api2pdf: read download: %w", err)
	}
	if resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("api2pdf: download server error %d", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, true, fmt.Errorf("api2pdf: download throttled")
	}
	if resp.StatusCode >= 400 {
		return nil, false, fmt.Errorf("api2pdf: download rejected %d", resp.StatusCode)
	}
	if !bytes.HasPrefix(respBody, []byte("%PDF-")) {
		return nil, false, fmt.Errorf("api2pdf: downloaded file is not a PDF")
	}
	return respBody, false, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
