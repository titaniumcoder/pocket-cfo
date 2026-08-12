package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

// fakeGitHub is a Contents API good enough to prove the pipeline, and — more
// importantly — records whether PUT was ever reached. Every rejection test
// asserts it wasn't: "the response said 400" is weaker than "nothing was
// committed".
type fakeGitHub struct {
	files    map[string][]byte
	puts     int
	lastBody []byte
	lastMsg  string
	lastSHA  string
	conflict bool // make the next PUT collide, as a racing writer would
}

func newFakeGitHub(files map[string]string) *fakeGitHub {
	f := &fakeGitHub{files: map[string][]byte{}}
	for k, v := range files {
		f.files[k] = []byte(v)
	}
	return f
}

func shaOf(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b))[:40] }

func (f *fakeGitHub) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/{owner}/{repo}/contents/{path...}", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want the configured token", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("X-GitHub-Api-Version = %q", got)
		}
		path := r.PathValue("path")

		switch r.Method {
		case http.MethodGet:
			body, ok := f.files[path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{
				"content": base64.StdEncoding.EncodeToString(body),
				"sha":     shaOf(body),
			})
		case http.MethodPut:
			f.puts++
			raw, _ := io.ReadAll(r.Body)
			var req putRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatalf("PUT body is not JSON: %v", err)
			}
			decoded, err := base64.StdEncoding.DecodeString(req.Content)
			if err != nil {
				t.Fatalf("PUT content is not base64: %v", err)
			}
			f.lastBody, f.lastMsg, f.lastSHA = decoded, req.Message, req.SHA
			if f.conflict {
				w.WriteHeader(http.StatusConflict)
				return
			}
			f.files[path] = decoded
			json.NewEncoder(w).Encode(map[string]any{"content": map[string]string{"sha": shaOf(decoded)}})
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

const writeBudgetJSON = `{
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "` + idRent + `", "name": "Rent", "amount": 900 },
      { "id": "` + idGroceries + `", "name": "Groceries", "amount": 350 }
    ]}
  ]
}`

func writeService(t *testing.T, gh *fakeGitHub) *Service {
	t.Helper()
	srv := gh.server(t)
	return &Service{
		Budget:  &tracker.Budget{FS: fstest.MapFS{"budget.json": &fstest.MapFile{Data: []byte(writeBudgetJSON)}}},
		Actuals: &tracker.Actuals{FS: fstest.MapFS{}},
		Store: &ContentsClient{
			HTTP: srv.Client(), Repo: "owner/data", Token: "test-token", BaseURL: srv.URL,
		},
	}
}

func doc(txs ...string) string {
	return `{
  "month": "2026-08",
  "coverage": [{ "account": "A", "from": "2026-08-01", "to": "2026-08-31", "imported_at": "2026-09-01" }],
  "transactions": [` + strings.Join(txs, ",") + `]
}`
}

func tx(id, date string, amount string, category string) string {
	return fmt.Sprintf(`{"id":%q,"date":%q,"description":"LINE %s","amount":%s,"account":"A","category":%q}`, id, date, id, amount, category)
}

const augustPath = "data/actuals/2026-08.json"

func put(t *testing.T, s *Service, month, document, baseSHA, allow string) (*PutResult, error) {
	t.Helper()
	return s.PutActuals(context.Background(), month, PutRequest{
		Document: json.RawMessage(document), BaseSHA: baseSHA, AllowRemovals: allow,
	})
}

func TestPutActualsCreatesANewMonth(t *testing.T) {
	gh := newFakeGitHub(nil)
	s := writeService(t, gh)

	got, err := put(t, s, "2026-08", doc(tx("t1", "2026-08-01", "900", idRent)), "", "")
	if err != nil {
		t.Fatalf("PutActuals: %v", err)
	}
	if got.SHA == "" || !got.DeployPending {
		t.Errorf("result = %+v", got)
	}
	if gh.puts != 1 {
		t.Fatalf("PUT called %d times, want 1", gh.puts)
	}
	if gh.lastSHA != "" {
		t.Errorf("sha = %q on create, want empty", gh.lastSHA)
	}

	// The committed bytes are a canonical re-marshal, so unvalidated keys can
	// never reach the repo and the git diff stays stable.
	if !strings.HasSuffix(string(gh.lastBody), "\n") {
		t.Error("committed body has no trailing newline")
	}
	var round map[string]any
	if err := json.Unmarshal(gh.lastBody, &round); err != nil {
		t.Fatalf("committed body is not JSON: %v", err)
	}
	if !strings.Contains(string(gh.lastBody), `  "month": "2026-08"`) {
		t.Errorf("committed body is not indented as expected:\n%s", gh.lastBody)
	}

	if !strings.HasPrefix(gh.lastMsg, "feat(actuals): reconcile 2026-08 through 2026-08-31") {
		t.Errorf("commit message = %q, want a Conventional Commit naming the coverage end", gh.lastMsg)
	}
	if strings.Contains(gh.lastMsg, "Allow-Removals") {
		t.Error("no override was used; the trailer must be absent")
	}
}

func TestPutActualsUpdatesAnExistingMonth(t *testing.T) {
	existing := doc(tx("t1", "2026-08-01", "900", idRent))
	gh := newFakeGitHub(map[string]string{augustPath: existing})
	s := writeService(t, gh)

	// Adding a transaction is the common case and must never trip the gate.
	body := doc(tx("t1", "2026-08-01", "900", idRent), tx("t2", "2026-08-03", "42.18", idGroceries))
	got, err := put(t, s, "2026-08", body, shaOf([]byte(existing)), "")
	if err != nil {
		t.Fatalf("PutActuals: %v", err)
	}
	if gh.puts != 1 || len(got.Changes) != 0 {
		t.Errorf("puts=%d changes=%v, want one clean write", gh.puts, got.Changes)
	}
	if gh.lastSHA == "" {
		t.Error("sha is empty on update, so the write wasn't conditional")
	}
}

// TestPutActualsRejectionsNeverCommit is the heart of it: for every way a
// submission can be refused, assert nothing reached the repo — not merely that
// the response said no.
func TestPutActualsRejectionsNeverCommit(t *testing.T) {
	existing := doc(
		tx("t1", "2026-08-01", "900", idRent),
		tx("t2", "2026-08-03", "42.18", idGroceries),
	)

	tests := []struct {
		name     string
		month    string
		document string
		baseSHA  string
		allow    string
		wantCode string
	}{
		{
			name:     "unknown category id",
			month:    "2026-08",
			document: doc(tx("t1", "2026-08-01", "900", "00000000-0000-4000-8000-00000000dead")),
			baseSHA:  shaOf([]byte(existing)),
			wantCode: CodeValidationFailed,
		},
		{
			name:     "a line with neither category nor ignored",
			month:    "2026-08",
			document: `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],"transactions":[{"id":"t1","date":"2026-08-01","description":"X","amount":900,"account":"A"}]}`,
			baseSHA:  shaOf([]byte(existing)),
			wantCode: CodeValidationFailed,
		},
		{
			name:     "date outside the month",
			month:    "2026-08",
			document: doc(tx("t1", "2026-09-01", "900", idRent)),
			baseSHA:  shaOf([]byte(existing)),
			wantCode: CodeValidationFailed,
		},
		{
			name:     "document month disagrees with the path",
			month:    "2026-08",
			document: strings.Replace(doc(tx("t1", "2026-08-01", "900", idRent)), `"month": "2026-08"`, `"month": "2026-09"`, 1),
			baseSHA:  shaOf([]byte(existing)),
			wantCode: CodeInvalidRequest,
		},
		{
			name:     "an unknown field",
			month:    "2026-08",
			document: `{"month":"2026-08","coverage":[],"transactions":[],"totals":{"x":1}}`,
			baseSHA:  shaOf([]byte(existing)),
			wantCode: CodeInvalidRequest,
		},
		{
			name:     "a malformed month in the path",
			month:    "2026-13",
			document: doc(tx("t1", "2026-08-01", "900", idRent)),
			baseSHA:  shaOf([]byte(existing)),
			wantCode: CodeInvalidRequest,
		},
		{
			name:     "a stale base_sha",
			month:    "2026-08",
			document: doc(tx("t1", "2026-08-01", "900", idRent), tx("t2", "2026-08-03", "42.18", idGroceries)),
			baseSHA:  "stale-and-wrong",
			wantCode: CodeConflict,
		},
		{
			name:     "a dropped transaction",
			month:    "2026-08",
			document: doc(tx("t1", "2026-08-01", "900", idRent)),
			baseSHA:  shaOf([]byte(existing)),
			wantCode: CodeWouldRemove,
		},
		{
			name:     "a mutated amount",
			month:    "2026-08",
			document: doc(tx("t1", "2026-08-01", "950", idRent), tx("t2", "2026-08-03", "42.18", idGroceries)),
			baseSHA:  shaOf([]byte(existing)),
			wantCode: CodeWouldRemove,
		},
		{
			name:  "shrunk coverage",
			month: "2026-08",
			document: `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-10","to":"2026-08-31","imported_at":"2026-09-01"}],` +
				`"transactions":[` + tx("t1", "2026-08-01", "900", idRent) + `,` + tx("t2", "2026-08-03", "42.18", idGroceries) + `]}`,
			baseSHA:  shaOf([]byte(existing)),
			wantCode: CodeWouldRemove,
		},
		{
			name:     "an empty document",
			month:    "2026-08",
			document: ``,
			baseSHA:  shaOf([]byte(existing)),
			wantCode: CodeInvalidRequest,
		},
		{
			name:     "a whitespace-only override does not count as a reason",
			month:    "2026-08",
			document: doc(tx("t1", "2026-08-01", "900", idRent)),
			baseSHA:  shaOf([]byte(existing)),
			allow:    "   ",
			wantCode: CodeWouldRemove,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := newFakeGitHub(map[string]string{augustPath: existing})
			s := writeService(t, gh)

			_, err := put(t, s, tt.month, tt.document, tt.baseSHA, tt.allow)
			if err == nil {
				t.Fatal("want an error")
			}
			e, ok := err.(*Error)
			if !ok {
				t.Fatalf("err = %T %v, want *Error", err, err)
			}
			if e.Code != tt.wantCode {
				t.Errorf("code = %q, want %q (%s)", e.Code, tt.wantCode, e.Message)
			}
			if gh.puts != 0 {
				t.Errorf("PUT was called %d times; nothing may reach the repo on a rejection", gh.puts)
			}
			if string(gh.files[augustPath]) != existing {
				t.Error("the committed month was modified despite the rejection")
			}
		})
	}
}

func TestPutActualsConflictCarriesTheCurrentSHA(t *testing.T) {
	existing := doc(tx("t1", "2026-08-01", "900", idRent))
	gh := newFakeGitHub(map[string]string{augustPath: existing})
	s := writeService(t, gh)

	_, err := put(t, s, "2026-08", existing, "wrong", "")
	e, ok := err.(*Error)
	if !ok || e.Code != CodeConflict {
		t.Fatalf("err = %v, want a conflict", err)
	}
	details, ok := e.Details.(map[string]string)
	if !ok || details["current_sha"] != shaOf([]byte(existing)) {
		t.Errorf("details = %v, want the current sha so Hermes can re-read", e.Details)
	}
}

func TestPutActualsAllowRemovalsAcceptsAndRecordsWhy(t *testing.T) {
	existing := doc(
		tx("t1", "2026-08-01", "900", idRent),
		tx("t2", "2026-08-03", "42.18", idGroceries),
	)
	gh := newFakeGitHub(map[string]string{augustPath: existing})
	s := writeService(t, gh)

	body := doc(tx("t1", "2026-08-01", "900", idRent))
	got, err := put(t, s, "2026-08", body, shaOf([]byte(existing)), "duplicate import of week 1")
	if err != nil {
		t.Fatalf("PutActuals: %v", err)
	}
	if gh.puts != 1 {
		t.Fatalf("PUT called %d times, want 1", gh.puts)
	}
	if !strings.Contains(gh.lastMsg, "Allow-Removals: duplicate import of week 1") {
		t.Errorf("commit message = %q, want the reason as a trailer so git log records it", gh.lastMsg)
	}
	if len(got.Changes) == 0 {
		t.Error("the response must say what was dropped, so an override is a visible decision")
	}
	if got.Applied != "duplicate import of week 1" {
		t.Errorf("Applied = %q", got.Applied)
	}
}

func TestPutActualsWritesNotConfigured(t *testing.T) {
	s := &Service{
		Budget:  &tracker.Budget{FS: fstest.MapFS{"budget.json": &fstest.MapFile{Data: []byte(writeBudgetJSON)}}},
		Actuals: &tracker.Actuals{FS: fstest.MapFS{}},
	}
	_, err := put(t, s, "2026-08", doc(tx("t1", "2026-08-01", "900", idRent)), "", "")
	e, ok := err.(*Error)
	if !ok || e.Code != CodeWriteNotConfigured {
		t.Fatalf("err = %v, want %s", err, CodeWriteNotConfigured)
	}
}

// TestPutActualsMapsAGitHubRace: GitHub can reject the write even when our
// base_sha matched at read time, if someone committed in between.
func TestPutActualsMapsAGitHubRace(t *testing.T) {
	existing := doc(tx("t1", "2026-08-01", "900", idRent))
	gh := newFakeGitHub(map[string]string{augustPath: existing})
	gh.conflict = true
	s := writeService(t, gh)

	body := doc(tx("t1", "2026-08-01", "900", idRent), tx("t2", "2026-08-03", "42.18", idGroceries))
	_, err := put(t, s, "2026-08", body, shaOf([]byte(existing)), "")
	e, ok := err.(*Error)
	if !ok || e.Code != CodeConflict {
		t.Fatalf("err = %v, want a conflict", err)
	}
}

// TestPutActualsNeverTouchesDataDir pins the single most important
// constraint: the app must not write its own DATA_DIR, an ephemeral mounted
// checkout whose changes would be lost on restart and diverge from git.
func TestPutActualsNeverTouchesDataDir(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"budget.json":          writeBudgetJSON,
		"actuals/2026-08.json": doc(tx("t1", "2026-08-01", "900", idRent)),
		"accounts.json":        `{"accounts":[]}`,
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before := hashTree(t, dir)

	gh := newFakeGitHub(nil)
	srv := gh.server(t)
	s := &Service{
		Budget:  &tracker.Budget{FS: os.DirFS(dir)},
		Actuals: &tracker.Actuals{FS: os.DirFS(dir)},
		Store:   &ContentsClient{HTTP: srv.Client(), Repo: "owner/data", Token: "test-token", BaseURL: srv.URL},
	}

	if _, err := put(t, s, "2026-08", doc(tx("t1", "2026-08-01", "900", idRent), tx("t2", "2026-08-04", "10", idGroceries)), "", ""); err != nil {
		t.Fatalf("PutActuals: %v", err)
	}
	if gh.puts != 1 {
		t.Fatalf("PUT called %d times, want 1 — the write must have happened for this test to mean anything", gh.puts)
	}

	if after := hashTree(t, dir); after != before {
		t.Error("PutActuals modified DATA_DIR; the checkout is ephemeral and git is the only store")
	}
}

func hashTree(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		fmt.Fprintf(h, "%s:%x\n", path, sha256.Sum256(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestActualsPath(t *testing.T) {
	s := &Service{}
	if got := s.actualsPath("2026-08"); got != augustPath {
		t.Errorf("default path = %q, want %q", got, augustPath)
	}
	s.ActualsPrefix = "actuals/"
	if got := s.actualsPath("2026-08"); got != "actuals/2026-08.json" {
		t.Errorf("overridden path = %q", got)
	}
}

func TestContentsClientGetNotFound(t *testing.T) {
	gh := newFakeGitHub(nil)
	srv := gh.server(t)
	c := &ContentsClient{HTTP: srv.Client(), Repo: "owner/data", Token: "test-token", BaseURL: srv.URL}
	if _, _, err := c.Get(context.Background(), "data/actuals/2026-01.json"); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound — an unwritten month is normal", err)
	}
}

// TestActualsForCarriesTheSHA closes the loop the documentation already
// described: put_actuals' own tool description says base_sha comes from
// get_actuals, and HERMES.md says to keep the sha from the read — while no
// read returned one. The only way to discover it was to provoke a 409, which
// is a conflict burned on every single write.
func TestActualsForCarriesTheSHA(t *testing.T) {
	const doc = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],"transactions":[]}`
	gh := newFakeGitHub(map[string]string{"data/actuals/2026-08.json": doc})
	srv := gh.server(t)
	s := &Service{
		Budget:  &tracker.Budget{FS: fstest.MapFS{}},
		Actuals: &tracker.Actuals{FS: fstest.MapFS{}},
		Store:   &ContentsClient{HTTP: srv.Client(), Repo: "owner/data", Token: "test-token", BaseURL: srv.URL},
	}

	got, err := s.ActualsFor(context.Background(), "2026-08")
	if err != nil {
		t.Fatalf("ActualsFor: %v", err)
	}
	if got.SHA == "" {
		t.Fatal("no sha; a write can only get one by provoking a conflict")
	}
	if want := shaOf([]byte(doc)); got.SHA != want {
		t.Errorf("sha = %q, want %q — the sha must name the document returned beside it", got.SHA, want)
	}
	if got.Month != "2026-08" {
		t.Errorf("month = %q", got.Month)
	}
}

// TestActualsForWithoutWritesHasNoSHA: with no store there is nothing to write
// to, so an empty sha is the honest answer rather than a fabricated one.
func TestActualsForWithoutWritesHasNoSHA(t *testing.T) {
	const doc = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],"transactions":[]}`
	s := &Service{
		Budget:  &tracker.Budget{FS: fstest.MapFS{}},
		Actuals: &tracker.Actuals{FS: fstest.MapFS{"actuals/2026-08.json": &fstest.MapFile{Data: []byte(doc)}}},
	}
	got, err := s.ActualsFor(context.Background(), "2026-08")
	if err != nil {
		t.Fatalf("ActualsFor: %v", err)
	}
	if got.SHA != "" {
		t.Errorf("sha = %q, want empty when writes are not configured", got.SHA)
	}
}
