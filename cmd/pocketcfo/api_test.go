package main

import (
	"encoding/json"
	"errors"
	"go/parser"
	"go/printer"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/api"
	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

const apiTestToken = "test-hermes-token"

const apiBudgetJSON = `{
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-8000-000000000001", "name": "Rent", "amount": 900 }
    ]},
    { "name": "Office", "kind": "company", "categories": [
      { "id": "00000000-0000-4000-8000-000000000002", "name": "Accounting", "amount": 150 }
    ]}
  ]
}`

const apiActualsJSON = `{
  "month": "2026-08",
  "coverage": [{ "account": "Private Checking", "from": "2026-08-01", "to": "2026-08-31", "imported_at": "2026-09-01" }],
  "transactions": [
    { "id": "a1", "date": "2026-08-01", "description": "LIDL SOFIA 4412", "amount": 42.18, "account": "Private Checking", "category": "00000000-0000-4000-8000-000000000001" }
  ]
}`

// apiServer builds a server with the Hermes routes configured, over in-memory
// data. token empty means the routes are never registered.
func apiServer(t *testing.T, token, env string) *server {
	t.Helper()
	budgetFS := fstest.MapFS{
		"budget.json":   &fstest.MapFile{Data: []byte(apiBudgetJSON)},
		"accounts.json": &fstest.MapFile{Data: []byte(`{"accounts":[{"name":"Private Checking","balance":100,"as_of":"2026-07-31"}]}`)},
	}
	actualsFS := fstest.MapFS{
		"actuals/2026-08.json": &fstest.MapFile{Data: []byte(apiActualsJSON)},
		// A month in a year that is not the current one, so a search whose
		// range names it can only be answered by deriving the year from that
		// range — see TestMCPAndRESTAgreeOnSearch.
		"actuals/2025-11.json": &fstest.MapFile{Data: []byte(apiOldActualsJSON)},
	}
	return &server{
		cfg: config{hermesAPIToken: token, env: env, sessionSecret: "test-secret"},
		tracker: &tracker.Tracker{
			Budget:   &tracker.Budget{FS: budgetFS},
			Accounts: &tracker.Accounts{FS: budgetFS},
			Actuals:  &tracker.Actuals{FS: actualsFS},
			Loc:      time.UTC,
		},
	}
}

const apiOldActualsJSON = `{
  "month": "2025-11",
  "coverage": [{ "account": "Private Checking", "from": "2025-11-01", "to": "2025-11-30", "imported_at": "2025-12-01" }],
  "transactions": [
    { "id": "old1", "date": "2025-11-04", "description": "LIDL SOFIA 4412", "amount": 180, "account": "Private Checking", "ignored": "before the plan" }
  ]
}`

func apiGet(t *testing.T, s *server, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

var apiPaths = []string{
	"/api/budget/categories",
	"/api/budget/2026-08",
	"/api/budget/2026",
	"/api/accounts",
	"/api/actuals/2026-08",
	"/api/transactions",
	"/api/reconciliation",
}

// TestAPIRoutesAbsentWithoutToken: unconfigured, no endpoint answers. env is
// deliberately "" — the tier where currentSession hands admin to every local
// request — so this also proves the routes aren't merely session-gated.
//
// The assertion is "serves no data", not "404": a two-segment path like
// /api/accounts falls through to /{year}/{month} when the API isn't
// registered, which is ordinary unknown-path behaviour rather than a leak.
func TestAPIRoutesAbsentWithoutToken(t *testing.T) {
	s := apiServer(t, "", "")
	for _, p := range apiPaths {
		w := apiGet(t, s, p, "")
		if w.Code == http.StatusOK {
			t.Errorf("%s = 200 with no token configured; body %s", p, w.Body)
		}
		for _, leak := range []string{"Rent", "Accounting", "LIDL", "Private Checking", "planned_cents"} {
			if strings.Contains(w.Body.String(), leak) {
				t.Errorf("%s leaked %q with no token configured", p, leak)
			}
		}
	}
}

func TestAPIRejectsMissingBearer(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")
	for _, p := range apiPaths {
		w := apiGet(t, s, p, "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", p, w.Code)
		}
		if got := w.Header().Get("WWW-Authenticate"); got != "Bearer" {
			t.Errorf("%s WWW-Authenticate = %q, want Bearer", p, got)
		}
		if strings.Contains(w.Body.String(), "Rent") {
			t.Errorf("%s leaked data in a 401 body", p)
		}
	}
}

func TestAPIRejectsWrongBearer(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")
	// Including a wrong token of identical length, so a length check alone
	// wouldn't pass this.
	sameLength := strings.Repeat("x", len(apiTestToken))
	for _, token := range []string{"nope", sameLength, apiTestToken + "x", strings.ToUpper(apiTestToken)} {
		w := apiGet(t, s, "/api/budget/categories", token)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("token %q = %d, want 401", token, w.Code)
		}
	}
}

// TestAPIIgnoresSessionCookie is the one that matters most: outside prod
// currentSession hands out an admin session to every local request, and the
// API must not inherit it.
func TestAPIIgnoresSessionCookie(t *testing.T) {
	s := apiServer(t, apiTestToken, "") // the dev-bypass tier
	encoded, err := auth.Encode(s.cfg.sessionSecret, auth.Session{Login: "someone", Permission: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/budget/categories", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: encoded})
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 — a session must never authenticate the API", w.Code)
	}
}

// TestAPISourceNeverConsultsSession pins the decision against a later
// refactor that "helpfully" reuses the page gate. Comments are stripped
// first, so documenting *why* the session is avoided doesn't trip it.
func TestAPISourceNeverConsultsSession(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "api.go", nil, 0) // 0: comments discarded
	if err != nil {
		t.Fatal(err)
	}
	var code strings.Builder
	if err := printer.Fprint(&code, fset, file); err != nil {
		t.Fatal(err)
	}
	// The qualified call forms, so this file's own apiAuthorized doesn't trip
	// a substring match on "authorized".
	for _, forbidden := range []string{"s.currentSession(", "s.authorized(", "s.authenticated(", "s.authenticatedForPart(", "sessionCookie"} {
		if strings.Contains(code.String(), forbidden) {
			t.Errorf("api.go references %s; the API must authenticate standalone", forbidden)
		}
	}
}

func TestAPIReadEndpoints(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")

	t.Run("categories", func(t *testing.T) {
		w := apiGet(t, s, "/api/budget/categories", apiTestToken)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", w.Code, w.Body)
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %q", ct)
		}
		var got []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("body is not JSON: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("got %d categories, want 2", len(got))
		}
	})

	t.Run("budget for a month", func(t *testing.T) {
		w := apiGet(t, s, "/api/budget/2026-08", apiTestToken)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", w.Code, w.Body)
		}
		var got struct {
			Month             string `json:"month"`
			TotalPrivateCents int    `json:"total_private_cents"`
			TotalCompanyCents int    `json:"total_company_cents"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Month != "2026-08" || got.TotalPrivateCents != 90000 || got.TotalCompanyCents != 15000 {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("budget for a year returns twelve buckets", func(t *testing.T) {
		w := apiGet(t, s, "/api/budget/2026", apiTestToken)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		var got []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 12 {
			t.Errorf("got %d months, want 12", len(got))
		}
	})

	t.Run("categories beats the period wildcard", func(t *testing.T) {
		// A literal segment must win over {period}, or "categories" would be
		// parsed as a month and 400.
		w := apiGet(t, s, "/api/budget/categories", apiTestToken)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want the literal route to win", w.Code)
		}
	})

	t.Run("actuals", func(t *testing.T) {
		w := apiGet(t, s, "/api/actuals/2026-08", apiTestToken)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "LIDL SOFIA 4412") {
			t.Error("body does not contain the transaction")
		}
	})

	t.Run("actuals for an unreconciled month is 404", func(t *testing.T) {
		w := apiGet(t, s, "/api/actuals/2026-07", apiTestToken)
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
		var body apiErrorBody
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error.Code != "not_found" {
			t.Errorf("code = %q, want not_found", body.Error.Code)
		}
	})

	t.Run("a malformed month is 400", func(t *testing.T) {
		w := apiGet(t, s, "/api/actuals/2026-13", apiTestToken)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	t.Run("transactions", func(t *testing.T) {
		w := apiGet(t, s, "/api/transactions?q=LIDL&year=2026", apiTestToken)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", w.Code, w.Body)
		}
		var got struct {
			Transactions []map[string]any `json:"transactions"`
			Truncated    bool             `json:"truncated"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Transactions) != 1 {
			t.Errorf("got %d matches, want 1", len(got.Transactions))
		}
	})

	t.Run("no match serialises as [] not null", func(t *testing.T) {
		w := apiGet(t, s, "/api/transactions?q=NOTHING&year=2026", apiTestToken)
		if !strings.Contains(w.Body.String(), `"transactions":[]`) {
			t.Errorf("body = %s, want an empty array", w.Body)
		}
	})

	t.Run("accounts", func(t *testing.T) {
		w := apiGet(t, s, "/api/accounts", apiTestToken)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Private Checking") {
			t.Errorf("status = %d body = %s", w.Code, w.Body)
		}
	})

	t.Run("reconciliation", func(t *testing.T) {
		w := apiGet(t, s, "/api/reconciliation?year=2026", apiTestToken)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		var got []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 12 {
			t.Errorf("got %d months, want 12", len(got))
		}
	})

	t.Run("a bad year is 400", func(t *testing.T) {
		if w := apiGet(t, s, "/api/reconciliation?year=abc", apiTestToken); w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	t.Run("an unknown path is a JSON 404, not the login page", func(t *testing.T) {
		w := apiGet(t, s, "/api/nope", apiTestToken)
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
		if !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
			t.Errorf("Content-Type = %q, want JSON", w.Header().Get("Content-Type"))
		}
	})

	t.Run("the fallback is still gated", func(t *testing.T) {
		if w := apiGet(t, s, "/api/nope", ""); w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 — the fallback must not leak the route surface", w.Code)
		}
	})
}

// TestAPIBudgetMatchesTheDashboard: the API drifting from the page is the
// failure that would quietly poison Hermes' matching.
func TestAPIBudgetMatchesTheDashboard(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")
	w := apiGet(t, s, "/api/budget/2026-08", apiTestToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got struct {
		TotalPrivateCents int `json:"total_private_cents"`
		TotalCompanyCents int `json:"total_company_cents"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	view, err := s.tracker.Budget.ForMonth(t.Context(), 2026, time.August, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalPrivateCents != view.TotalPlannedCents {
		t.Errorf("API private %d, dashboard %d", got.TotalPrivateCents, view.TotalPlannedCents)
	}
	if got.TotalCompanyCents != view.CompanyTotalPlannedCents {
		t.Errorf("API company %d, dashboard %d", got.TotalCompanyCents, view.CompanyTotalPlannedCents)
	}
}

// TestAPIRoutesDoNotShadowThePages guards the other direction: adding /api/
// must leave the existing routes reachable.
func TestAPIRoutesDoNotShadowThePages(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")
	mux := s.routes()
	for _, p := range []string{"/", "/2026", "/2026/8", "/invoicing", "/info", "/auth/login"} {
		r := httptest.NewRequest(http.MethodGet, p, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code == http.StatusNotFound {
			t.Errorf("%s = 404; the API registration shadowed a page", p)
		}
	}
}

// TestDecodeYears covers what the adapter still owns: turning ?year= strings
// into ints. Which years a from/to span covers moved to api.Service, where
// both adapters get the same answer — see TestScanYears.
func TestDecodeYears(t *testing.T) {
	tests := []struct {
		name     string
		explicit []string
		want     []int
		wantErr  bool
	}{
		{name: "none", want: nil},
		{name: "explicit", explicit: []string{"2026"}, want: []int{2026}},
		{name: "several", explicit: []string{"2024", "2026"}, want: []int{2024, 2026}},
		{name: "not a number", explicit: []string{"abc"}, wantErr: true},
		{name: "out of range", explicit: []string{"12"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeYears(tt.explicit)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// TestAPIDoesNotWriteDataDir pins that every read endpoint is read-only.
func TestAPIDoesNotWriteDataDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "canary"), []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	s := apiServer(t, apiTestToken, "prod")
	for _, p := range apiPaths {
		apiGet(t, s, p, apiTestToken)
	}

	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("the data dir gained or lost files: %d then %d", len(before), len(after))
	}
	b, err := os.ReadFile(filepath.Join(dir, "canary"))
	if err != nil || string(b) != "untouched" {
		t.Error("the canary file was modified")
	}
}

// putServer wires a server with writes configured. The GitHub call itself is
// covered in internal/api against a fake Contents API; these tests are about
// the adapter's own decoding, limits and status mapping.
func putServer(t *testing.T) *server {
	t.Helper()
	s := apiServer(t, apiTestToken, "prod")
	s.cfg.githubDataToken = "gh-token"
	s.cfg.repo = "owner/data"
	s.httpClient = &http.Client{}
	return s
}

func apiPut(t *testing.T, s *server, month, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut, "/api/actuals/"+month, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

func TestAPIPutRequiresABearer(t *testing.T) {
	s := putServer(t)
	w := apiPut(t, s, "2026-08", `{"document":{},"base_sha":""}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestAPIPutWithoutAWriteTokenIs503(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod") // no githubDataToken
	body := `{"document":{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],"transactions":[]},"base_sha":""}`
	w := apiPut(t, s, "2026-08", body, apiTestToken)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body %s", w.Code, w.Body)
	}
	var e apiErrorBody
	json.Unmarshal(w.Body.Bytes(), &e)
	if e.Error.Code != "write_not_configured" {
		t.Errorf("code = %q", e.Error.Code)
	}
}

func TestAPIPutRejectsAnOversizedBody(t *testing.T) {
	s := putServer(t)
	w := apiPut(t, s, "2026-08", `{"document":`+strings.Repeat("a", 2<<20)+`}`, apiTestToken)
	if w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want the body cap to reject it", w.Code)
	}
}

func TestAPIPutRejectsAnUnknownEnvelopeField(t *testing.T) {
	s := putServer(t)
	w := apiPut(t, s, "2026-08", `{"document":{},"base_sha":"","typo":1}`, apiTestToken)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unknown field", w.Code)
	}
}

func TestAPIPutMapsServiceCodesToStatuses(t *testing.T) {
	tests := []struct {
		name, month, body string
		want              int
	}{
		{
			name: "bad month", month: "2026-13",
			body: `{"document":{"month":"2026-13","coverage":[],"transactions":[]},"base_sha":""}`,
			want: http.StatusBadRequest,
		},
		{
			name: "validation failure", month: "2026-08",
			body: `{"document":{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],` +
				`"transactions":[{"id":"x","date":"2026-08-01","description":"X","amount":1,"account":"A","category":"00000000-0000-4000-8000-0000000000ff"}]},"base_sha":""}`,
			want: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := putServer(t)
			w := apiPut(t, s, tt.month, tt.body, apiTestToken)
			if w.Code != tt.want {
				t.Errorf("status = %d, want %d, body %s", w.Code, tt.want, w.Body)
			}
			if !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
				t.Errorf("Content-Type = %q, want JSON", w.Header().Get("Content-Type"))
			}
		})
	}
}

func TestAPIStatusMapping(t *testing.T) {
	tests := []struct {
		code string
		want int
	}{
		{api.CodeInvalidRequest, http.StatusBadRequest},
		{api.CodeValidationFailed, http.StatusBadRequest},
		{api.CodeWouldRemove, http.StatusBadRequest},
		{api.CodeNotFound, http.StatusNotFound},
		{api.CodeConflict, http.StatusConflict},
		{api.CodeWriteNotConfigured, http.StatusServiceUnavailable},
		{api.CodeUpstream, http.StatusBadGateway},
		{api.CodeInternal, http.StatusInternalServerError},
	}
	for _, tt := range tests {
		if got := apiStatus(&api.Error{Code: tt.code}); got != tt.want {
			t.Errorf("%s = %d, want %d", tt.code, got, tt.want)
		}
	}
	if got := apiStatus(errors.New("plain")); got != http.StatusInternalServerError {
		t.Errorf("a non-api error = %d, want 500", got)
	}
}
