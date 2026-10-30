package render

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A test binary that exits immediately, so every failure path that depends on
// chromium dying is cheap and network-free.
func writeDeadChromium(t *testing.T, stderrLine string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "chromium")
	script := "#!/bin/sh\n" + stderrLine + "\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func testRenderer(t *testing.T) *Chromium {
	t.Helper()
	bin := os.Getenv("CHROMIUM_BIN")
	if bin == "" {
		for _, name := range chromiumNames {
			if path, err := exec.LookPath(name); err == nil {
				bin = path
				break
			}
		}
	}
	if bin == "" {
		t.Skip("no chrome-family browser on this machine; the CDP round trip needs a real one")
	}
	c, err := NewChromium(bin)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestChromium_RendersHTMLToPDF(t *testing.T) {
	c := testRenderer(t)

	pdf, err := c.Render(context.Background(), []byte("<html><body><h1>Rechnung ФАКТУРА</h1></body></html>"))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Errorf("output is not a PDF: %q", pdf[:8])
	}
	if len(pdf) < 1000 {
		t.Errorf("rendered only %d bytes — a blank page, not the document", len(pdf))
	}
}

func TestChromium_WaitsForPagedJSBeforePrinting(t *testing.T) {
	c := testRenderer(t)

	// Three screens of text through the real paged.js pipeline: printing at
	// page load, the way the CLI flags do it, captures one blank page — the
	// polyfill parks the body in a <template> until it has laid the pages out.
	var body strings.Builder
	for i := 0; i < 200; i++ {
		body.WriteString("<p>Zeile der Rechnung ФАКТУРА строка</p>")
	}
	html := `<html><head>
<link href="https://fonts.googleapis.com/css2?family=PT+Sans:wght@400;700&display=block" rel="stylesheet">
<script src="https://cdn.jsdelivr.net/npm/pagedjs@0.4.3/dist/paged.polyfill.js" crossorigin="anonymous"></script>
<style>@page{size:A4;margin:20mm}</style>
</head><body>` + body.String() + `</body></html>`

	pdf, err := c.Render(context.Background(), []byte(html))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if pages := pdfPageCount(pdf); pages < 2 {
		t.Errorf("rendered %d page(s); the paged.js layout pass did not run", pages)
	}
}

func pdfPageCount(pdf []byte) int {
	return bytes.Count(pdf, []byte("/Type /Page")) - bytes.Count(pdf, []byte("/Type /Pages"))
}

func TestChromium_CarriesTheBrowserStderrOnFailure(t *testing.T) {
	bin := writeDeadChromium(t, `echo "no display" >&2`)
	c, err := NewChromium(bin)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Render(context.Background(), []byte("<html/>"))
	if err == nil {
		t.Fatal("a dying browser must fail the render")
	}
	if !strings.Contains(err.Error(), "no display") {
		t.Errorf("error does not carry the browser stderr: %v", err)
	}
}

func TestChromium_RespectsACancelledContext(t *testing.T) {
	c := testRenderer(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Render(ctx, []byte("<html/>")); err == nil {
		t.Error("a cancelled context must abort the render")
	}
}

func TestChromium_FindsAChromiumOnThePath(t *testing.T) {
	bin := writeDeadChromium(t, "")
	t.Setenv("PATH", filepath.Dir(bin))

	c, err := NewChromium("")
	if err != nil {
		t.Fatalf("empty bin should fall back to LookPath: %v", err)
	}
	if c.Bin != bin {
		t.Errorf("looked up %q, want %q", c.Bin, bin)
	}
}

func TestChromium_RefusesToStartWithoutABrowser(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := NewChromium(""); err == nil {
		t.Error("no browser on PATH must refuse to build a renderer")
	}
}

func TestPipeConn_FramesOnNULAndReportsProtocolErrors(t *testing.T) {
	conn := newPipeConn(strings.NewReader(
		`{"id":1,"result":{}}`+"\x00"+
			`{"id":2,"error":{"message":"not allowed"}}`+"\x00"),
		nilWriter{})

	if _, err := conn.call("X.test", nil); err != nil {
		t.Fatalf("first call: %v", err)
	}
	_, err := conn.call("X.test", nil)
	if err == nil || err.Error() != "not allowed" {
		t.Errorf("second call: got %v, want the protocol error", err)
	}
}

type nilWriter struct{}

func (nilWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestChromium_RetryExhaustionMentionsTheAttempts(t *testing.T) {
	bin := writeDeadChromium(t, "true")
	c, err := NewChromium(bin)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Render(context.Background(), []byte("<html/>"))
	if err == nil || !strings.Contains(err.Error(), "3 attempts") {
		t.Errorf("want retry exhaustion, got %v", err)
	}
}

var _ Renderer = (*Chromium)(nil)
