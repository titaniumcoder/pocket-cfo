package render

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const chromiumBudget = 10000 * time.Millisecond

// maxRetries lives in api2pdf.go until that renderer is deleted.

var chromiumNames = []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"}

type Chromium struct {
	Bin string
}

func NewChromium(bin string) (*Chromium, error) {
	if bin == "" {
		for _, name := range chromiumNames {
			if path, err := exec.LookPath(name); err == nil {
				bin = path
				break
			}
		}
	}
	if bin == "" {
		return nil, fmt.Errorf("chromium: no chrome binary found (install chromium or set CHROMIUM_BIN)")
	}
	return &Chromium{Bin: bin}, nil
}

func (c *Chromium) Render(ctx context.Context, html []byte) ([]byte, error) {
	dir, err := os.MkdirTemp("", "pocketcfo-render")
	if err != nil {
		return nil, fmt.Errorf("chromium: temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	input := filepath.Join(dir, "invoice.html")
	if err := os.WriteFile(input, html, 0o600); err != nil {
		return nil, fmt.Errorf("chromium: write html: %w", err)
	}
	output := filepath.Join(dir, "invoice.pdf")

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

		err := c.attempt(ctx, input, output)
		if err == nil {
			pdf, err := os.ReadFile(output)
			if err != nil {
				return nil, fmt.Errorf("chromium: read pdf: %w", err)
			}
			if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
				return nil, fmt.Errorf("chromium: output is not a PDF")
			}
			return pdf, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("chromium: giving up after %d attempts: %w", maxRetries, lastErr)
}

func (c *Chromium) attempt(ctx context.Context, input, output string) error {
	args := []string{
		"--headless",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		"--no-pdf-header-footer",
		"--virtual-time-budget=" + strconv.FormatInt(chromiumBudget.Milliseconds(), 10),
		"--print-to-pdf=" + output,
		"file://" + input,
	}
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("chromium: %w: %s", err, truncateStderr(stderr.Bytes()))
	}
	return nil
}

func truncateStderr(b []byte) string {
	const n = 500
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
