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

// Renderer turns HTML into a PDF. See ARCHITECTURE.md §6.
type Renderer interface {
	Render(ctx context.Context, html []byte) ([]byte, error)
}

const api2pdfEndpoint = "https://v2.api2pdf.com/chrome/pdf/html"
const api2pdfBalanceEndpoint = "https://v2.api2pdf.com/balance"

// fontDelay gives the api2pdf Chrome container time to fetch the
// Noto Sans webfont before it snapshots the page — their Chrome is a fresh
// serverless container per call, so the font is fetched cold every time.
// See ARCHITECTURE.md §6.
const fontDelay = 1500 * time.Millisecond

const maxRetries = 3

// API2PDF renders HTML to PDF via api2pdf's Chrome endpoint.
type API2PDF struct {
	APIKey string
	Client *http.Client
}

// NewAPI2PDF builds an API2PDF renderer. apiKey must be non-empty.
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
	HTML    string         `json:"html"`
	Options api2pdfOptions `json:"options"`
}

// api2pdfResponse is what the REST endpoint actually returns: a JSON
// envelope with a link to the rendered file, valid for 24 hours.
// "outputBinary" only affects the official SDKs, which fetch FileUrl for
// you — the raw HTTP API always responds with this envelope, never the PDF
// bytes inline.
type api2pdfResponse struct {
	Success bool   `json:"Success"`
	Error   string `json:"Error"`
	FileUrl string `json:"FileUrl"`
}

// Render posts html to api2pdf's Chrome endpoint and returns the rendered
// PDF bytes. Retries with backoff on 5xx per ARCHITECTURE.md §6.
func (r *API2PDF) Render(ctx context.Context, html []byte) ([]byte, error) {
	if r.APIKey == "" {
		return nil, fmt.Errorf("api2pdf: API key is empty")
	}

	body, err := json.Marshal(api2pdfRequest{
		HTML:    string(html),
		Options: api2pdfOptions{Delay: fontDelay.Milliseconds()},
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

// BalanceInfo is api2pdf's GET /balance response, parsed defensively: the
// exact field names aren't publicly documented — their own official Node
// SDK just passes the raw JSON through, untyped (confirmed by reading its
// source: utilityBalance() returns `Promise<string | Buffer | null>`, no
// parsed shape at all). Raw holds every top-level field (stringified) so
// the /info diagnostics page can always show *something* even if the field
// names below turn out wrong for a given account; Balance/HasBalance is a
// best-effort extraction of whichever field looks like the account balance.
type BalanceInfo struct {
	Balance    float64
	HasBalance bool
	Currency   string
	Raw        map[string]string
}

// Balance fetches the account's api2pdf balance for the /info diagnostics
// page — a read-only GET, same base URL/Authorization-header convention as
// Render above.
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
		// Every numeric field is formatted here, not just the one we
		// recognize as the balance: api2pdf's real field names aren't
		// documented, so whichever key actually carries the money is very
		// likely one we didn't anticipate — and printing it as a bare Go
		// float ("12.5") next to properly formatted figures is exactly the
		// inconsistency this avoids.
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
	if resp.StatusCode >= 400 {
		return nil, false, fmt.Errorf("api2pdf: request rejected %d: %s", resp.StatusCode, truncate(respBody, 500))
	}

	var parsed api2pdfResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, false, fmt.Errorf("api2pdf: unexpected response: %s", truncate(respBody, 500))
	}
	if !parsed.Success || parsed.FileUrl == "" {
		return nil, false, fmt.Errorf("api2pdf: %s", parsed.Error)
	}

	return r.fetch(ctx, parsed.FileUrl)
}

// fetch downloads the rendered PDF from the file URL api2pdf returns —
// their REST API hands back a link, valid 24 hours, not the bytes inline.
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
