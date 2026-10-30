package render

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type Renderer interface {
	Render(ctx context.Context, html []byte) ([]byte, error)
}

// printDeadline bounds one chromium invocation in real time. paged.js needs
// about a second for a long invoice; the deadline is generous because a cold
// start also fetches the font stylesheet and the paged.js polyfill from their
// CDNs (§6).
const printDeadline = 60 * time.Second

// nativeFallbackAfter is how long to wait for the paged.js polyfill to show
// up before printing the un-paginated page anyway. When the CDN is
// unreachable the template degrades to native Chrome printing on purpose
// (§6); without this wait that degradation would be an error instead. The
// polyfill defines window.Paged within about a second when the CDN answers,
// so a wait much longer than this only taxes bulk renders.
const nativeFallbackAfter = 5 * time.Second

// pagePollInterval paces the layout poll: a finished paged.js run must show
// the same page count twice in a row before the print is trusted.
const pagePollInterval = 250 * time.Millisecond

const maxRetries = 3

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

		pdf, retryable, err := c.print(ctx, html)
		if err == nil {
			return pdf, nil
		}
		lastErr = err
		if !retryable {
			return nil, err
		}
	}
	return nil, fmt.Errorf("chromium: giving up after %d attempts: %w", maxRetries, lastErr)
}

func (c *Chromium) print(ctx context.Context, html []byte) (pdf []byte, retryable bool, err error) {
	dir, err := os.MkdirTemp("", "pocketcfo-render")
	if err != nil {
		return nil, false, fmt.Errorf("chromium: temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	input := filepath.Join(dir, "invoice.html")
	if err := os.WriteFile(input, html, 0o600); err != nil {
		return nil, false, fmt.Errorf("chromium: write html: %w", err)
	}

	cmd := exec.CommandContext(ctx, c.Bin,
		"--headless",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		"--no-first-run",
		"--remote-debugging-pipe",
		"--user-data-dir="+filepath.Join(dir, "profile"),
	)
	cmd.Stderr = &bytes.Buffer{}

	childEnds, stdin, stdout, err := debugPipes(cmd)
	if err != nil {
		return nil, false, fmt.Errorf("chromium: debug pipes: %w", err)
	}
	defer stdin.Close()
	defer stdout.Close()

	// The browser's ends of both pipes must be closed on our side right after
	// start, or EOF never reaches the reader even after the browser dies — a
	// dead browser then costs a full printDeadline per call instead of an
	// instant write error.
	if err := cmd.Start(); err != nil {
		childEnds.close()
		return nil, true, fmt.Errorf("chromium: start %s: %w", c.Bin, err)
	}
	childEnds.close()
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	session, conn, err := openPage(stdout, stdin, "file://"+input)
	if err != nil {
		return nil, true, fmt.Errorf("chromium: %w (%s)", err, cmd.Stderr.(*bytes.Buffer).String())
	}

	pdfData, err := printWhenSettled(ctx, conn, session)
	if err != nil {
		return nil, true, fmt.Errorf("chromium: %w (%s)", err, cmd.Stderr.(*bytes.Buffer).String())
	}
	return pdfData, false, nil
}

// debugPipes wires the two os.Pipe pairs chrome's --remote-debugging-pipe
// protocol expects: chrome reads commands from fd 3 and writes replies and
// events to fd 4. The returned childEnds are the browser's ends; they must be
// closed on our side once the process is running.
func debugPipes(cmd *exec.Cmd) (childEnds *pipeEnds, stdin, stdout *os.File, err error) {
	toChromeR, toChromeW, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, err
	}
	fromChromeR, fromChromeW, err := os.Pipe()
	if err != nil {
		toChromeR.Close()
		toChromeW.Close()
		return nil, nil, nil, err
	}
	cmd.ExtraFiles = []*os.File{toChromeR, fromChromeW}
	return &pipeEnds{toChromeR, fromChromeW}, toChromeW, fromChromeR, nil
}

type pipeEnds [2]*os.File

func (e *pipeEnds) close() {
	for _, f := range e {
		_ = f.Close()
	}
}

func openPage(stdout io.Reader, stdin io.Writer, url string) (string, *pipeConn, error) {
	conn := newPipeConn(stdout, stdin)

	target, err := conn.call("Target.createTarget", map[string]any{"url": url})
	if err != nil {
		return "", conn, fmt.Errorf("create target: %w", err)
	}
	attached, err := conn.call("Target.attachToTarget", map[string]any{
		"targetId": target["targetId"],
		"flatten":  true,
	})
	if err != nil {
		return "", conn, fmt.Errorf("attach to target: %w", err)
	}
	session, _ := attached["sessionId"].(string)
	return session, conn, nil
}

// printWhenSettled polls the laid-out page until paged.js has finished — the
// page count stops growing between two polls — and only then prints. Printing
// earlier captures the document mid-pagination, which prints an empty page:
// the polyfill parks the whole body inside a <template> while it works.
func printWhenSettled(ctx context.Context, conn *pipeConn, session string) ([]byte, error) {
	start := time.Now()
	deadline := start.Add(printDeadline)
	fallback := start.Add(nativeFallbackAfter)
	lastPages, stable := 0, 0

	for {
		state, err := pollPages(conn, session)
		if err != nil {
			return nil, err
		}
		if state.pages > 0 && state.pages == lastPages {
			stable++
			if stable >= 2 {
				break
			}
		} else {
			stable = 0
		}
		lastPages = state.pages

		if !state.paged && time.Now().After(fallback) {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("paged.js did not settle within %s (%d pages so far)", printDeadline, state.pages)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pagePollInterval):
		}
	}

	result, err := conn.callAs(session, "Page.printToPDF", map[string]any{
		"preferCSSPageSize": true,
		"printBackground":   true,
		"marginTop":         0,
		"marginBottom":      0,
		"marginLeft":        0,
		"marginRight":       0,
	})
	if err != nil {
		return nil, fmt.Errorf("print: %w", err)
	}
	encoded, _ := result["data"].(string)
	pdf, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("print: decode pdf: %w", err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return nil, fmt.Errorf("print: output is not a PDF")
	}
	return pdf, nil
}

type pageState struct {
	pages int
	paged bool
}

func pollPages(conn *pipeConn, session string) (pageState, error) {
	result, err := conn.callAs(session, "Runtime.evaluate", map[string]any{
		"expression": `(() => ({
			pages: document.querySelectorAll(".pagedjs_pages .pagedjs_page").length,
			paged: typeof window.Paged !== "undefined",
		}))()`,
		"returnByValue": true,
	})
	if err != nil {
		return pageState{}, fmt.Errorf("inspect page: %w", err)
	}
	envelope, _ := result["result"].(map[string]any)
	value, _ := envelope["value"].(map[string]any)
	state := pageState{}
	if n, ok := value["pages"].(float64); ok {
		state.pages = int(n)
	}
	state.paged, _ = value["paged"].(bool)
	return state, nil
}

// pipeConn speaks chrome's --remote-debugging-pipe protocol: NUL-terminated
// JSON frames, commands written to the browser, replies and events read back.
// Events are dropped — the render loop only needs request/response.

type pipeConn struct {
	r *bufio.Reader
	w io.Writer

	mu     sync.Mutex
	nextID int
	msgs   chan cdpMessage
}

type cdpMessage struct {
	ID     int            `json:"id"`
	Result map[string]any `json:"result,omitempty"`
	Error  *cdpError      `json:"error,omitempty"`
}

type cdpError struct {
	Message string `json:"message"`
}

func (e *cdpError) Error() string { return e.Message }

func newPipeConn(r io.Reader, w io.Writer) *pipeConn {
	c := &pipeConn{r: bufio.NewReaderSize(r, 1<<20), w: w, msgs: make(chan cdpMessage, 64)}
	go c.read()
	return c
}

func (c *pipeConn) read() {
	defer close(c.msgs)
	for {
		raw, err := c.r.ReadBytes(0)
		if err != nil {
			return
		}
		var msg cdpMessage
		if err := json.Unmarshal(bytes.TrimSuffix(raw, []byte{0}), &msg); err != nil {
			continue
		}
		c.msgs <- msg
	}
}

func (c *pipeConn) call(method string, params any) (map[string]any, error) {
	return c.callAs("", method, params)
}

func (c *pipeConn) callAs(sessionID, method string, params any) (map[string]any, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	frame := map[string]any{"id": id, "method": method, "params": params}
	if sessionID != "" {
		frame["sessionId"] = sessionID
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", method, err)
	}
	if _, err := c.w.Write(append(encoded, 0)); err != nil {
		return nil, fmt.Errorf("send %s: %w", method, err)
	}

	for {
		select {
		case msg, ok := <-c.msgs:
			if !ok {
				return nil, fmt.Errorf("%s: chromium closed the debug connection", method)
			}
			if msg.ID != id {
				continue
			}
			if msg.Error != nil {
				return nil, msg.Error
			}
			return msg.Result, nil
		case <-time.After(printDeadline):
			return nil, fmt.Errorf("%s: timed out", method)
		}
	}
}

func (c *pipeConn) close() {}
