package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
)

func newLoginTestServer(t *testing.T) *server {
	t.Helper()
	s := newFinanceTestServer(t)
	s.cfg.stateDir = filepath.Join(t.TempDir(), "state")
	return s
}

func writeUsersList(t *testing.T, entries string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(usersFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(usersFile, []byte(entries), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestALoginIsRecordedAndSurvivesARestart: the info page's figure comes from
// a store that outlives the process — a fresh store over the same state
// directory reports the same last login and count.
func TestALoginIsRecordedAndSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	newLoginStore(dir).record("person@example.com", time.Now())
	newLoginStore(dir).record("person@example.com", time.Now().Add(time.Minute))

	if _, err := os.Stat(filepath.Join(dir, "logins.json")); err != nil {
		t.Fatalf("logins.json not written: %v", err)
	}
	var recs map[string]loginRecord
	raw, _ := os.ReadFile(filepath.Join(dir, "logins.json"))
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatal(err)
	}
	if recs["person@example.com"].Count != 2 {
		t.Errorf("count = %d, want 2", recs["person@example.com"].Count)
	}
	if recs["person@example.com"].Last.IsZero() {
		t.Error("last = zero, want the most recent login")
	}
}

// TestTheLoginsGroupTellsActiveFromExpired: a login seen inside the session
// TTL reads as active, one whose sessions have all expired reads as expired,
// and a listed user who never logged in says so.
func TestTheLoginsGroupTellsActiveFromExpired(t *testing.T) {
	s := newLoginTestServer(t)
	writeUsersList(t, `{"users":[
		{"email":"active@example.com","parts":["finance"]},
		{"email":"gone@example.com","parts":["finance"]},
		{"email":"quiet@example.com","parts":["invoicing"]}
	]}`)

	st := s.logins()
	st.record("active@example.com", time.Now())
	st.record("gone@example.com", time.Now().Add(-auth.ReadOnlyTTL*2))

	group := configGroup{Name: "Logins", Rows: s.loginRows()}
	byName := map[string]string{}
	for _, r := range group.Rows {
		byName[r.Name] = r.Value
	}
	if got := byName["active@example.com (finance)"]; !strings.Contains(got, "last login") || !strings.Contains(got, "active") {
		t.Errorf("active row = %q, want last login marked active", got)
	}
	if got := byName["gone@example.com (finance)"]; !strings.Contains(got, "session expired") {
		t.Errorf("expired row = %q, want session expired", got)
	}
	if got := byName["quiet@example.com (invoicing)"]; got != "never logged in" {
		t.Errorf("quiet row = %q, want never logged in", got)
	}
}

// TestWithoutAStateDirTheLoginsStayInMemory: no STATE_DIR, no file — the
// store still reports this run's logins.
func TestWithoutAStateDirTheLoginsStayInMemory(t *testing.T) {
	s := newFinanceTestServer(t)
	s.cfg.stateDir = ""
	writeUsersList(t, `{"users":[{"email":"person@example.com","parts":["finance"]}]}`)

	s.logins().record("person@example.com", time.Now())

	rows := s.loginRows()
	if len(rows) != 1 || !strings.Contains(rows[0].Value, "last login") {
		t.Errorf("rows = %+v, want the in-memory login", rows)
	}
}

// TestAViewerRequestMarksItsLoginActive: using the site with a read-only
// session keeps that login active on the info page without touching disk.
func TestAViewerRequestMarksItsLoginActive(t *testing.T) {
	s := newLoginTestServer(t)
	writeUsersList(t, `{"users":[{"email":"someone@example.com","parts":["finance"]}]}`)

	s.logins().record("someone@example.com", time.Now().Add(-auth.ReadOnlyTTL/2))
	// A session granted just now: the login time is old, but the session lives.
	r := readOnlyRequest(t, s, "/2026/10", []string{"finance"})
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("finance page for a viewer = %d, want 200: %s", w.Code, w.Body.String()[:min(200, w.Body.Len())])
	}

	rows := s.loginRows()
	if len(rows) == 0 || !strings.Contains(rows[0].Value, "active") {
		t.Errorf("rows = %+v, want the login marked active after use", rows)
	}
}
