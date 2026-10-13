package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	"github.com/titaniumcoder/pocket-cfo/internal/llm"
)

func uploadsServer(t *testing.T, parserAnswer string, github map[string][]byte) *server {
	t.Helper()
	s := apiServer(t, apiTestToken, "prod")
	s.cfg.sessionSecret = "test-secret"
	s.cfg.uploadsDir = t.TempDir()
	s.uploads = &uploadsStore{dir: s.cfg.uploadsDir}
	s.uploadJobs = newUploadJobs()
	s.uploadsTmpl = mustPageTemplate(filepath.Join("..", "..", "templates", "uploads.html"))
	if parserAnswer != "" {
		s.cfg.openAIKey = "sk-test"
		s.cfg.openAIModel = "stub/model"
		s.llm = parserStub(t, parserAnswer)
	} else {
		s.llm = nil
	}
	if github != nil {
		s.cfg.githubDataToken = "gh-test"
		s.cfg.repo = "datum"
		s.cfg.githubAPIURL = githubStub(t, github).URL
	}
	return s
}

func uploadsRequest(t *testing.T, s *server, method, path, permission string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, body)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if permission != "" {
		encoded, err := auth.Encode(s.cfg.sessionSecret, auth.NewSession("octocat", permission, time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: encoded})
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

func multipartBody(t *testing.T, files map[string][]byte, account string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if account != "" {
		w.WriteField("account", account)
	}
	for name, data := range files {
		fw, err := w.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(data)
	}
	w.Close()
	return &buf, w.FormDataContentType()
}

func parserStub(t *testing.T, answer string) *llm.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":"+jsonString(answer)+"},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	client, err := llm.NewClient(llm.ClientConfig{Key: "sk-test", BaseURL: srv.URL, Model: "stub/model"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

type putCall struct {
	Path    string
	Body    string
	Message string
}

func githubStub(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	githubPuts = nil
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/repos/datum/contents/") {
			http.NotFound(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/repos/datum/contents/")
		switch r.Method {
		case http.MethodGet:
			content, ok := files[path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"content": base64.StdEncoding.EncodeToString(content),
				"sha":     "basesha",
			})
		case http.MethodPut:
			var body struct {
				Content string `json:"content"`
				Message string `json:"message"`
			}
			raw, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("bad PUT body: %v", err)
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			decoded, err := base64.StdEncoding.DecodeString(body.Content)
			if err != nil {
				t.Errorf("bad PUT content: %v", err)
			}
			githubPuts = append(githubPuts, putCall{Path: path, Body: string(decoded), Message: body.Message})
			files[path] = decoded
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"content": map[string]any{"sha": "newsha"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

var githubPuts []putCall

const parseAnswer = `[
  {"date":"2026-08-03","description":"LIDL SOFIA 4412","amount":42.18,"account":"Private Checking","category":"00000000-0000-4000-8000-000000000001"},
  {"date":"2026-08-04","description":"STRANGE THING","amount":7.5,"account":"Private Checking","category":""},
  {"date":"not-a-date","description":"GARBAGE","amount":1,"account":"x","category":""}
]`

func waitForStatus(t *testing.T, s *server, statuses ...string) uploadMeta {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		uploads, err := s.uploads.list()
		if err != nil {
			t.Fatal(err)
		}
		for _, meta := range uploads {
			for _, want := range statuses {
				if meta.Status == want {
					return meta
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	uploads, _ := s.uploads.list()
	for _, meta := range uploads {
		t.Logf("upload %s: %q (%s)", meta.ID, meta.Status, meta.Error)
	}
	t.Fatalf("no upload reached %v", statuses)
	return uploadMeta{}
}

func TestUploadsAreForAdminsOnly(t *testing.T) {
	s := uploadsServer(t, "", nil)
	for _, tc := range []struct {
		permission string
		want       int
	}{
		{"", http.StatusFound},
		{"readonly", http.StatusForbidden},
		{"push", http.StatusForbidden},
		{"admin", http.StatusOK},
	} {
		w := uploadsRequest(t, s, http.MethodGet, "/uploads", tc.permission, nil, "")
		if w.Code != tc.want {
			t.Errorf("%q: /uploads = %d, want %d", tc.permission, w.Code, tc.want)
		}
	}
}

func TestAnUploadIsStoredAndParsed(t *testing.T) {
	s := uploadsServer(t, parseAnswer, nil)
	body, contentType := multipartBody(t, map[string][]byte{
		"august.csv": []byte("date;description;amount\n2026-08-03;LIDL SOFIA 4412;-42.18\n"),
	}, "Private Checking")
	w := uploadsRequest(t, s, http.MethodPost, "/uploads", "admin", body, contentType)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST /uploads = %d, body %s", w.Code, w.Body)
	}
	meta := waitForStatus(t, s, "parsed")
	if meta.Name != "august.csv" || meta.Account != "Private Checking" || meta.UploadedBy != "octocat" {
		t.Errorf("meta = %+v", meta)
	}
	if len(meta.Transactions) != 2 {
		t.Fatalf("%d staged transactions, want 2: %+v (%s)", len(meta.Transactions), meta.Transactions, meta.Error)
	}
	if _, err := os.Stat(filepath.Join(s.cfg.uploadsDir, meta.ID, "august.csv")); err != nil {
		t.Errorf("the original is not kept in the inbox: %v", err)
	}
}

func TestAnUploadTheModelCannotClassifyIsStagedUntracked(t *testing.T) {
	s := uploadsServer(t, parseAnswer, nil)
	body, contentType := multipartBody(t, map[string][]byte{"august.csv": []byte("x")}, "")
	uploadsRequest(t, s, http.MethodPost, "/uploads", "admin", body, contentType)
	meta := waitForStatus(t, s, "parsed")
	if meta.Transactions[1].Category != "" || meta.Transactions[1].Untracked == "" {
		t.Errorf("an empty category must stage as untracked: %+v", meta.Transactions[1])
	}
}

func TestAnUploadWithNoUsableLinesFailsAndStaysInTheInbox(t *testing.T) {
	s := uploadsServer(t, `{"date":"nope"}`, nil)
	body, contentType := multipartBody(t, map[string][]byte{"weird.csv": []byte("x")}, "")
	uploadsRequest(t, s, http.MethodPost, "/uploads", "admin", body, contentType)
	meta := waitForStatus(t, s, "failed")
	if meta.Error == "" {
		t.Error("a failed parse must say why")
	}
	if _, err := os.Stat(filepath.Join(s.cfg.uploadsDir, meta.ID, "weird.csv")); err != nil {
		t.Errorf("a failed parse must keep the file for a retry: %v", err)
	}
}

func TestUploadsRejectWhatTheyCannotRead(t *testing.T) {
	s := uploadsServer(t, "", nil)
	body, contentType := multipartBody(t, map[string][]byte{"virus.exe": []byte("MZ")}, "")
	w := uploadsRequest(t, s, http.MethodPost, "/uploads", "admin", body, contentType)
	if w.Code != http.StatusBadRequest {
		t.Errorf("an unknown extension = %d, want 400", w.Code)
	}
	big := strings.Repeat("x", maxUploadFile+1)
	body, contentType = multipartBody(t, map[string][]byte{"big.csv": []byte(big)}, "")
	w = uploadsRequest(t, s, http.MethodPost, "/uploads", "admin", body, contentType)
	if w.Code != http.StatusBadRequest {
		t.Errorf("an oversized file = %d, want 400", w.Code)
	}
}

func TestConfirmCommitsToTheDataRepoAndDeduplicates(t *testing.T) {
	existing := map[string][]byte{"data/actuals/2026-08.json": []byte(apiActualsJSON)}
	s := uploadsServer(t, parseAnswer, existing)
	body, contentType := multipartBody(t, map[string][]byte{"august.csv": []byte("x")}, "")
	uploadsRequest(t, s, http.MethodPost, "/uploads", "admin", body, contentType)
	meta := waitForStatus(t, s, "parsed")

	w := uploadsRequest(t, s, http.MethodPost, "/uploads/"+meta.ID+"/confirm", "admin", nil, "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("confirm = %d, body %s", w.Code, w.Body)
	}
	if len(githubPuts) != 1 {
		t.Fatalf("%d commits, want 1: %+v", len(githubPuts), githubPuts)
	}
	if githubPuts[0].Path != "data/actuals/2026-08.json" {
		t.Errorf("committed %s", githubPuts[0].Path)
	}
	if !strings.Contains(githubPuts[0].Message, "august.csv") {
		t.Errorf("commit message: %q", githubPuts[0].Message)
	}
	var af struct {
		Month        string `json:"month"`
		Transactions []struct {
			ID     string  `json:"id"`
			Date   string  `json:"date"`
			Amount float64 `json:"amount"`
		} `json:"transactions"`
	}
	if err := json.Unmarshal([]byte(githubPuts[0].Body), &af); err != nil {
		t.Fatalf("the committed file does not parse: %v\n%s", err, githubPuts[0].Body)
	}
	if af.Month != "2026-08" || len(af.Transactions) != 3 {
		t.Fatalf("committed %s with %d transactions: %s", af.Month, len(af.Transactions), githubPuts[0].Body)
	}
	for _, tx := range af.Transactions {
		if tx.ID == "" {
			t.Error("a committed transaction must carry a derived id")
		}
	}
	for _, id := range []string{"a1"} {
		found := false
		for _, tx := range af.Transactions {
			found = found || tx.ID == id
		}
		if !found {
			t.Errorf("the existing transaction %s was not preserved", id)
		}
	}

	// Re-uploading the same statement is idempotent: the derived ids match what
	// is already in the file, so the second confirm commits nothing.
	githubPuts = nil
	body, contentType = multipartBody(t, map[string][]byte{"august.csv": []byte("x")}, "")
	uploadsRequest(t, s, http.MethodPost, "/uploads", "admin", body, contentType)
	again := waitForStatus(t, s, "parsed")
	if again.ID == meta.ID {
		t.Fatal("the second upload must be its own upload")
	}
	uploadsRequest(t, s, http.MethodPost, "/uploads/"+again.ID+"/confirm", "admin", nil, "")
	if len(githubPuts) != 0 {
		t.Errorf("a re-uploaded statement re-committed %d file(s)", len(githubPuts))
	}
}

func TestConfirmWithoutWritesIsRefused(t *testing.T) {
	s := uploadsServer(t, parseAnswer, nil)
	body, contentType := multipartBody(t, map[string][]byte{"august.csv": []byte("x")}, "")
	uploadsRequest(t, s, http.MethodPost, "/uploads", "admin", body, contentType)
	meta := waitForStatus(t, s, "parsed")
	w := uploadsRequest(t, s, http.MethodPost, "/uploads/"+meta.ID+"/confirm", "admin", nil, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("confirm without GITHUB_DATA_TOKEN = %d, want 503", w.Code)
	}
}

func TestAStatementSpanningTwoMonthsCommitsTwoFiles(t *testing.T) {
	existing := map[string][]byte{}
	s := uploadsServer(t, `[
		{"date":"2026-08-31","description":"RENT","amount":900,"account":"Private Checking","category":"00000000-0000-4000-8000-000000000001"},
		{"date":"2026-09-01","description":"GROCERIES","amount":30,"account":"Private Checking","category":""}
	]`, existing)
	body, contentType := multipartBody(t, map[string][]byte{"statement.csv": []byte("x")}, "")
	uploadsRequest(t, s, http.MethodPost, "/uploads", "admin", body, contentType)
	meta := waitForStatus(t, s, "parsed")
	w := uploadsRequest(t, s, http.MethodPost, "/uploads/"+meta.ID+"/confirm", "admin", nil, "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("confirm = %d, body %s", w.Code, w.Body)
	}
	paths := []string{}
	for _, put := range githubPuts {
		paths = append(paths, put.Path)
	}
	if len(paths) != 2 || paths[0] != "data/actuals/2026-08.json" || paths[1] != "data/actuals/2026-09.json" {
		t.Errorf("commits = %v, want one per month", paths)
	}
}

func TestCategoriesCanBeCorrectedBeforeConfirm(t *testing.T) {
	s := uploadsServer(t, parseAnswer, map[string][]byte{})
	body, contentType := multipartBody(t, map[string][]byte{"august.csv": []byte("x")}, "")
	uploadsRequest(t, s, http.MethodPost, "/uploads", "admin", body, contentType)
	meta := waitForStatus(t, s, "parsed")

	form := url.Values{"cat-1": {"00000000-0000-4000-8000-000000000002"}}
	w := uploadsRequest(t, s, http.MethodPost, "/uploads/"+meta.ID+"/category", "admin", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("category edit = %d, body %s", w.Code, w.Body)
	}
	meta, err := s.uploads.load(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Transactions[1].Category != "00000000-0000-4000-8000-000000000002" || meta.Transactions[1].Untracked != "" {
		t.Errorf("the correction did not stick: %+v", meta.Transactions[1])
	}
}

func TestAPurgeEmptiesTheInbox(t *testing.T) {
	s := uploadsServer(t, "", nil)
	body, contentType := multipartBody(t, map[string][]byte{"august.csv": []byte("x")}, "")
	uploadsRequest(t, s, http.MethodPost, "/uploads", "admin", body, contentType)
	if s.uploads.count() != 1 {
		t.Fatal("the upload did not land")
	}
	w := uploadsRequest(t, s, http.MethodPost, "/uploads/purge", "admin", nil, "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("purge = %d", w.Code)
	}
	if n := s.uploads.count(); n != 0 {
		t.Errorf("%d upload(s) survived the purge", n)
	}
}

func TestUploadNamesAreSanitised(t *testing.T) {
	s := uploadsServer(t, "", nil)
	body, contentType := multipartBody(t, map[string][]byte{"../../etc/passwd.csv": []byte("x")}, "")
	w := uploadsRequest(t, s, http.MethodPost, "/uploads", "admin", body, contentType)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("a path-like name = %d, body %s", w.Code, w.Body)
	}
	uploads, err := s.uploads.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(uploads) != 1 || uploads[0].Name != "passwd.csv" || strings.Contains(uploads[0].Name, "/") {
		t.Errorf("stored name = %q", uploads[0].Name)
	}
	if _, err := os.Stat(filepath.Join(s.cfg.uploadsDir, uploads[0].ID, "passwd.csv")); err != nil {
		t.Errorf("the original did not land inside the upload's own directory: %v", err)
	}
}

func TestAnUploadCannotBeTouchedWhileItIsParsing(t *testing.T) {
	s := uploadsServer(t, parseAnswer, nil)
	body, contentType := multipartBody(t, map[string][]byte{"august.csv": []byte("x")}, "")
	uploadsRequest(t, s, http.MethodPost, "/uploads", "admin", body, contentType)
	s.uploadJobs.start("whatever") // blocks nothing; we simulate by marking an id active below

	// Wait until the real parse is over, then prove the guard fires on a
	// genuinely running job.
	meta := waitForStatus(t, s, "parsed")
	s.uploadJobs.start(meta.ID)
	form := url.Values{"cat-0": {"untracked"}}
	w := uploadsRequest(t, s, http.MethodPost, "/uploads/"+meta.ID+"/category", "admin", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if w.Code != http.StatusConflict {
		t.Errorf("editing while parsing = %d, want 409", w.Code)
	}
}

func TestUploadsStatusIsJSONForThePollingPage(t *testing.T) {
	s := uploadsServer(t, "", nil)
	w := uploadsRequest(t, s, http.MethodGet, "/uploads/status", "admin", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content type %q", ct)
	}
	var out struct {
		Uploads []uploadMeta `json:"uploads"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("the status is not JSON: %v", err)
	}
}

func TestAnIDThatIsNotAnUploadIDIsNotFound(t *testing.T) {
	s := uploadsServer(t, "", nil)
	w := uploadsRequest(t, s, http.MethodPost, "/uploads/zzzzzzzz/category", "admin", nil, "")
	if w.Code != http.StatusNotFound {
		t.Errorf("a malformed id = %d, want 404", w.Code)
	}
}

func TestParseWithoutAKeyRefuses(t *testing.T) {
	s := uploadsServer(t, "", nil)
	body, contentType := multipartBody(t, map[string][]byte{"august.csv": []byte("x")}, "")
	uploadsRequest(t, s, http.MethodPost, "/uploads", "admin", body, contentType)
	uploads, err := s.uploads.list()
	if err != nil {
		t.Fatal(err)
	}
	if uploads[0].Status != "pending" {
		t.Fatalf("status = %q, want pending", uploads[0].Status)
	}
	w := uploadsRequest(t, s, http.MethodPost, "/uploads/"+uploads[0].ID+"/parse", "admin", nil, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("parse without a key = %d, want 503", w.Code)
	}
}

func TestDerivedIDsMatchTheSchemaConventionOnCollision(t *testing.T) {
	tx := stagedTransaction{Date: "2026-08-03", Description: "LIDL", Amount: 5, Account: "A"}.transaction()
	first, duplicate := deriveTransactionID(tx, map[string]bool{})
	if duplicate {
		t.Fatal("a fresh transaction is not a duplicate")
	}
	taken := map[string]bool{first: true}
	_, duplicate = deriveTransactionID(tx, taken)
	if !duplicate {
		t.Error("the same content again must be recognised as a duplicate")
	}
	other := tx
	other.Description = "BILLA"
	third, duplicate := deriveTransactionID(other, taken)
	if duplicate || third == first {
		t.Errorf("a different transaction = %q; changing the description must change the id", third)
	}
}
