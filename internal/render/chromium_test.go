package render

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeChromium = `#!/bin/sh
for arg in "$@"; do
	case "$arg" in
		--print-to-pdf=*)
			out="${arg#--print-to-pdf=}"
			printf '%%PDF-fake\n' > "$out"
			;;
		file://*)
			cat "${arg#file://}" > "$(dirname "$0")/last-input.txt"
			;;
	esac
done
`

func writeFakeChromium(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	// Named "chromium" so NewChromium's empty-bin LookPath fallback finds it.
	path := filepath.Join(dir, "chromium")
	script := fakeChromium + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestChromium_RendersPDFThroughTheBinary(t *testing.T) {
	bin := writeFakeChromium(t, "")
	c, err := NewChromium(bin)
	if err != nil {
		t.Fatal(err)
	}

	pdf, err := c.Render(context.Background(), []byte("<html>hello</html>"))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.HasPrefix(string(pdf), "%PDF-") {
		t.Errorf("rendered %q, want a PDF", pdf)
	}

	lastInput, err := os.ReadFile(filepath.Join(filepath.Dir(bin), "last-input.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lastInput), "<html>hello</html>") {
		t.Errorf("the html did not reach the browser input file: %s", lastInput)
	}
}

func TestChromium_RetriesWhenTheBrowserCrashes(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "runs")
	bin := filepath.Join(dir, "fake-chromium")
	script := `#!/bin/sh
echo x >> ` + counter + `
if [ "$(wc -l < ` + counter + `)" -lt 2 ]; then
	echo "crashed" >&2
	exit 1
fi
for arg in "$@"; do
	case "$arg" in
		--print-to-pdf=*) printf '%%PDF-fake\n' > "${arg#--print-to-pdf=}" ;;
	esac
done
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	c, err := NewChromium(bin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Render(context.Background(), []byte("<html/>")); err != nil {
		t.Fatalf("second attempt should have succeeded: %v", err)
	}
}

func TestChromium_RefusesOutputThatIsNotAPDF(t *testing.T) {
	bin := writeFakeChromium(t, `printf 'not a pdf\n' > "$out"`)
	c, err := NewChromium(bin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Render(context.Background(), []byte("<html/>")); err == nil {
		t.Error("non-PDF output passed the prefix check")
	}
}

func TestChromium_ReportsTheBrowserStderr(t *testing.T) {
	bin := writeFakeChromium(t, `echo "fonts blew up" >&2
exit 3`)
	c, err := NewChromium(bin)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Render(context.Background(), []byte("<html/>"))
	if err == nil {
		t.Fatal("a failing browser must fail the render")
	}
	if !strings.Contains(err.Error(), "fonts blew up") {
		t.Errorf("error does not carry the browser stderr: %v", err)
	}
}

func TestChromium_FindsAChromiumOnThePath(t *testing.T) {
	bin := writeFakeChromium(t, "")
	dir := filepath.Dir(bin)
	t.Setenv("PATH", dir)

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
