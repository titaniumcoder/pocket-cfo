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

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
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

// TestWritesNeverTouchDataDir: DATA_DIR is an ephemeral mounted checkout, so
// a write that landed there would be lost on restart and diverge from git.
// Both endpoints go through the Store or nowhere.
func TestWritesNeverTouchDataDir(t *testing.T) {
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

	gh := newFakeGitHub(map[string]string{augustPath: doc(tx("t1", "2026-08-01", "900", idRent))})
	srv := gh.server(t)
	s := &Service{
		Budget:  &tracker.Budget{FS: os.DirFS(dir)},
		Actuals: &tracker.Actuals{FS: os.DirFS(dir)},
		Store:   &ContentsClient{HTTP: srv.Client(), Repo: "owner/data", Token: "test-token", BaseURL: srv.URL},
	}

	if _, err := add(t, s, AddRequest{
		Transactions: []actualsdata.Transaction{addTx("t2", "2026-08-04", 10, idGroceries)},
	}); err != nil {
		t.Fatalf("AddTransactions: %v", err)
	}
	if _, err := edit(t, s, EditRequest{Edits: []Edit{
		{ID: "t1", Month: "2026-08", Category: strp(idGroceries)},
	}}); err != nil {
		t.Fatalf("EditTransactions: %v", err)
	}
	if gh.puts != 2 {
		t.Fatalf("PUT called %d times, want 2 — the writes must have happened for this test to mean anything", gh.puts)
	}

	if after := hashTree(t, dir); after != before {
		t.Error("a write modified DATA_DIR; the checkout is ephemeral and git is the only store")
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
