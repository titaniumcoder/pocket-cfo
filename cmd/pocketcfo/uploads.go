package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/api"
	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
	"github.com/titaniumcoder/pocket-cfo/internal/llm"
	"github.com/titaniumcoder/pocket-cfo/internal/webui"
)

const (
	maxUploadFile  = 8 << 20
	maxUploadBody  = 33 << 20
	uploadTimeout  = 9 * time.Minute
	untrackedNote  = "not recognised at import"
	importedStatus = "imported"
)

var uploadExtension = regexp.MustCompile(`(?i)\.(csv|txt|pdf|png|jpe?g|webp|gif|heic)$`)

type uploadMeta struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Size         int64               `json:"size"`
	UploadedAt   time.Time           `json:"uploaded_at"`
	UploadedBy   string              `json:"uploaded_by"`
	Account      string              `json:"account,omitempty"`
	Status       string              `json:"status"`
	Error        string              `json:"error,omitempty"`
	Model        string              `json:"model,omitempty"`
	Transactions []stagedTransaction `json:"transactions,omitempty"`
	ImportedTo   []string            `json:"imported_to,omitempty"`
}

type stagedTransaction struct {
	Date        string  `json:"date"`
	Description string  `json:"description"`
	Amount      float64 `json:"amount"`
	Account     string  `json:"account"`
	Category    string  `json:"category,omitempty"`
	Untracked   string  `json:"untracked,omitempty"`
}

func (t stagedTransaction) month() string { return t.Date[:7] }

func (t stagedTransaction) transaction() actualsdata.Transaction {
	tx := actualsdata.Transaction{
		Date:        t.Date,
		Description: t.Description,
		Amount:      t.Amount,
		Account:     t.Account,
	}
	if t.Untracked != "" {
		tx.Untracked = &t.Untracked
	} else {
		category := t.Category
		tx.Category = &category
	}
	return tx
}

type uploadsStore struct{ dir string }

func mustOpenUploads(cfg config) *uploadsStore {
	dir := cfg.uploadsDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "pocketcfo-uploads")
		log.Printf("pocketcfo: neither STATE_DIR nor UPLOADS_DIR is set — uploads are kept in %s and do NOT survive a restart; mount a volume and set STATE_DIR", dir)
	}
	store := &uploadsStore{dir: dir}
	if err := store.open(); err != nil {
		log.Fatalf("pocketcfo: %v", err)
	}
	return store
}

const parserHTTPTimeout = 200 * time.Second

func mustOpenParser(cfg config, shared *http.Client) *llm.Client {
	http := *shared
	http.Timeout = parserHTTPTimeout
	client, err := llm.NewClient(llm.ClientConfig{
		Key: cfg.openAIKey, BaseURL: cfg.openAIBaseURL, Model: cfg.openAIModel,
		ExtraBody: cfg.openAIExtraBody, Referer: cfg.baseURL, HTTP: &http,
	})
	if err != nil {
		log.Fatalf("pocketcfo: %v", err)
	}
	log.Printf("pocketcfo: uploads are parsed with %s at %s", cfg.openAIModel, cfg.openAIBaseURL)
	return client
}

func (u uploadsStore) open() error { return os.MkdirAll(u.dir, 0o700) }

func (u uploadsStore) save(name string, data []byte, account, by string) (uploadMeta, error) {
	name = sanitizeUploadName(name)
	if !uploadExtension.MatchString(name) {
		return uploadMeta{}, fmt.Errorf("%s is not a statement pocket-cfo can read (csv, txt, pdf or a photo)", name)
	}
	id := ""
	for try := 0; try < 3; try++ {
		id = uploadID()
		if _, err := os.Stat(filepath.Join(u.dir, id)); errors.Is(err, fs.ErrNotExist) {
			break
		}
	}
	dir := filepath.Join(u.dir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return uploadMeta{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		return uploadMeta{}, err
	}
	meta := uploadMeta{
		ID: id, Name: name, Size: int64(len(data)),
		UploadedAt: time.Now().UTC(), UploadedBy: by, Account: account,
		Status: "pending",
	}
	return meta, u.saveMeta(meta)
}

func uploadID() string {
	var b [4]byte
	rand.Read(b[:])
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

func sanitizeUploadName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name = strings.TrimSpace(b.String())
	if name == "" || name == "." || name == ".." {
		name = "upload"
	}
	return name
}

func (u uploadsStore) saveMeta(m uploadMeta) error {
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(u.dir, m.ID, "meta.json"), body, 0o600)
}

func (u uploadsStore) load(id string) (uploadMeta, error) {
	if !validUploadID(id) {
		return uploadMeta{}, fs.ErrNotExist
	}
	body, err := os.ReadFile(filepath.Join(u.dir, id, "meta.json"))
	if err != nil {
		return uploadMeta{}, err
	}
	var m uploadMeta
	if err := json.Unmarshal(body, &m); err != nil {
		return uploadMeta{}, err
	}
	return m, nil
}

func validUploadID(id string) bool {
	if len(id) != len("20060102-150405-xxxxxxxx") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r == '-':
		default:
			return false
		}
	}
	return true
}

func (u uploadsStore) originalPath(m uploadMeta) string {
	return filepath.Join(u.dir, m.ID, m.Name)
}

func (u uploadsStore) list() ([]uploadMeta, error) {
	entries, err := os.ReadDir(u.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []uploadMeta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := u.load(e.Name())
		if err != nil {
			log.Printf("uploads: skipping %s: %v", e.Name(), err)
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UploadedAt.After(out[j].UploadedAt) })
	return out, nil
}

func (u uploadsStore) count() int {
	entries, err := os.ReadDir(u.dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n
}

func (u uploadsStore) delete(id string) error {
	if !validUploadID(id) {
		return fs.ErrNotExist
	}
	return os.RemoveAll(filepath.Join(u.dir, id))
}

func (u uploadsStore) purge() (int, error) {
	entries, err := os.ReadDir(u.dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			if err := os.RemoveAll(filepath.Join(u.dir, e.Name())); err == nil {
				n++
			}
		}
	}
	return n, nil
}

type uploadJobs struct {
	mu     sync.Mutex
	active map[string]bool
}

func newUploadJobs() *uploadJobs { return &uploadJobs{active: map[string]bool{}} }

func (j *uploadJobs) start(id string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.active[id] {
		return false
	}
	j.active[id] = true
	return true
}

func (j *uploadJobs) done(id string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.active, id)
}

func (j *uploadJobs) running(id string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.active[id]
}

func (s *server) registerUploads(mux *http.ServeMux) {
	mux.HandleFunc("GET /uploads", s.handleUploads)
	mux.HandleFunc("POST /uploads", s.handleUploadPost)
	mux.HandleFunc("GET /uploads/status", s.handleUploadsStatus)
	mux.HandleFunc("POST /uploads/{id}/category", s.handleUploadCategory)
	mux.HandleFunc("POST /uploads/{id}/confirm", s.handleUploadConfirm)
	mux.HandleFunc("POST /uploads/{id}/parse", s.handleUploadParse)
	mux.HandleFunc("POST /uploads/{id}/delete", s.handleUploadDelete)
	mux.HandleFunc("POST /uploads/purge", s.handleUploadsPurge)
}

func (s *server) uploadsSession(w http.ResponseWriter, r *http.Request) (auth.Session, bool) {
	sess, ok := s.currentSession(r)
	if !ok || !s.authenticated(sess) {
		s.rememberDestination(w, r)
		http.Redirect(w, r, "/auth/login", http.StatusFound)
		return sess, false
	}
	if !s.admin(sess) {
		http.Error(w, "uploads are for administrators only", http.StatusForbidden)
		return sess, false
	}
	return sess, true
}

type uploadsView struct {
	Header     webui.Header
	Uploads    []uploadMeta
	Categories []api.Category
	Parsing    bool
	Writes     bool
	Model      string
}

func (s *server) handleUploads(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.uploadsSession(w, r)
	if !ok {
		return
	}
	uploads, err := s.uploads.list()
	if err != nil {
		serverError(w, r, "listing uploads", err)
		return
	}
	view := uploadsView{
		Header:  s.header(sess, webui.PageUploads, webui.ParsePeriod(r.URL.Query().Get("year"), r.URL.Query().Get("month"))),
		Uploads: uploads,
		Parsing: s.llm != nil,
		Writes:  s.apiService().Store != nil,
	}
	if s.llm != nil {
		view.Model = s.llm.Model()
	}
	if view.Writes {
		categories, err := s.apiService().Categories(r.Context())
		if err != nil {
			serverError(w, r, "reading budget categories", err)
			return
		}
		view.Categories = categories
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.uploadsTmpl.Execute(w, view); err != nil {
		serverError(w, r, "rendering uploads", err)
	}
}

func (s *server) handleUploadPost(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.uploadsSession(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
	if err := r.ParseMultipartForm(maxUploadBody); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "that is more than the uploads page accepts at once", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "the upload must be a multipart form", http.StatusBadRequest)
		return
	}
	account := strings.TrimSpace(r.FormValue("account"))
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		http.Error(w, "choose at least one file", http.StatusBadRequest)
		return
	}
	saved := 0
	for _, fh := range files {
		data, err := readUploadFile(w, fh)
		if err != nil {
			http.Error(w, fmt.Sprintf("%s: %v", fh.Filename, err), http.StatusBadRequest)
			return
		}
		meta, err := s.uploads.save(fh.Filename, data, account, sess.Login)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		saved++
		s.maybeStartParse(meta)
	}
	log.Printf("uploads: %d file(s) received from %s", saved, sess.Login)
	http.Redirect(w, r, "/uploads", http.StatusSeeOther)
}

func readUploadFile(w http.ResponseWriter, fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxUploadFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxUploadFile {
		return nil, fmt.Errorf("is larger than the %d MB a single upload accepts", maxUploadFile>>20)
	}
	return data, nil
}

func (s *server) maybeStartParse(meta uploadMeta) {
	if s.llm == nil {
		return
	}
	if !s.uploadJobs.start(meta.ID) {
		return
	}
	go s.parseUpload(meta)
}

func (s *server) parseUpload(meta uploadMeta) {
	defer s.uploadJobs.done(meta.ID)
	meta.Status, meta.Error = "parsing", ""
	s.saveMetaOrLog(meta)

	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
	defer cancel()

	err := s.parseUploadContext(ctx, &meta)
	if err != nil {
		meta.Status, meta.Error = "failed", err.Error()
	} else {
		meta.Status, meta.Error = "parsed", ""
	}
	meta.Model = s.llm.Model()
	s.saveMetaOrLog(meta)
	log.Printf("uploads: %s (%s) parsed: %s", meta.ID, meta.Name, meta.Status)
}

func (s *server) saveMetaOrLog(m uploadMeta) {
	if err := s.uploads.saveMeta(m); err != nil {
		log.Printf("uploads: saving %s: %v", m.ID, err)
	}
}

type parsedLine struct {
	Date        string  `json:"date"`
	Description string  `json:"description"`
	Amount      float64 `json:"amount"`
	Account     string  `json:"account"`
	Category    string  `json:"category"`
}

const parseSystemPrompt = `You read bank statements, card statements and payment confirmations and turn them into transaction records for a personal-finance tracker.

Answer with ONLY a JSON array — no prose, no code fences. One object per statement line, in statement order:
{"date":"YYYY-MM-DD","description":"<the line as the statement wrote it>","amount":<number>,"account":"<account or card>","category":"<budget category id or empty string>"}

Rules:
- amount is in euros; positive is money LEAVING the account (a payment), negative is money arriving (salary, refund, transfer in). Never 0.
- date is the booking date, YYYY-MM-DD.
- description repeats the statement line verbatim, including merchant codes; do not translate or clean it.
- account is the account or card the statement covers when the document names one; otherwise use the default account the user gave.
- category is one of the budget category ids the user listed, chosen as accurately as you can. When you are not sure, use the empty string — the user reviews every line anyway, and a wrong guess is worse than an honest empty one.
- Include EVERY line of the document, including incoming money and fees. Never invent lines that are not in the document.`

func (s *server) parseUploadContext(ctx context.Context, meta *uploadMeta) error {
	categories, err := s.apiService().Categories(ctx)
	if err != nil {
		return fmt.Errorf("reading budget categories: %v", err)
	}
	if len(categories) == 0 {
		return errors.New("the budget has no categories to assign")
	}
	original, err := os.ReadFile(s.uploads.originalPath(*meta))
	if err != nil {
		return err
	}
	known := map[string]bool{}
	var list strings.Builder
	for _, c := range categories {
		known[c.ID] = true
		fmt.Fprintf(&list, "%s — %s / %s\n", c.ID, c.Group, c.Name)
	}

	message := []llm.Message{
		{Role: "system", Content: parseSystemPrompt},
		{Role: "user", Parts: uploadParts(*meta, original, list.String())},
	}
	out, err := s.llm.Complete(ctx, message, nil)
	if err != nil {
		return err
	}
	lines, err := decodeParsedLines(out.Message.Content)
	if err != nil {
		return err
	}
	staged, problems := stageParsedLines(lines, *meta, known)
	if len(staged) == 0 {
		if len(problems) > 0 {
			return fmt.Errorf("the model returned no usable lines: %s", strings.Join(problems, "; "))
		}
		return errors.New("the model returned no transactions")
	}
	if len(problems) > 0 {
		log.Printf("uploads: %s: %d unusable line(s): %s", meta.ID, len(problems), strings.Join(problems, "; "))
	}
	meta.Transactions = staged
	return nil
}

func uploadParts(meta uploadMeta, original []byte, categories string) []llm.Part {
	intro := "Default account when the document names none: " + orDefault(meta.Account, "unknown") + ".\n\nBudget categories:\n" + categories
	switch {
	case strings.HasSuffix(strings.ToLower(meta.Name), ".pdf"):
		return []llm.Part{llm.TextPart(intro), llm.FilePart(meta.Name, "application/pdf", original)}
	case uploadExtension.MatchString(meta.Name) && !strings.HasSuffix(strings.ToLower(meta.Name), ".csv") && !strings.HasSuffix(strings.ToLower(meta.Name), ".txt"):
		mime := uploadMIME(meta.Name)
		return []llm.Part{llm.TextPart(intro), llm.ImagePart(mime, original)}
	default:
		return []llm.Part{llm.TextPart(intro + "\n\nThe document:\n\n" + string(original))}
	}
}

func uploadMIME(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".heic":
		return "image/heic"
	}
	return "application/octet-stream"
}

func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

var jsonFence = regexp.MustCompile("(?s)```[a-z]*\\n(.*?)```")

func decodeParsedLines(content string) ([]parsedLine, error) {
	trimmed := strings.TrimSpace(content)
	if m := jsonFence.FindStringSubmatch(trimmed); m != nil {
		trimmed = strings.TrimSpace(m[1])
	}
	start, end := strings.Index(trimmed, "["), strings.LastIndex(trimmed, "]")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("the answer holds no JSON array: %.200s", trimmed)
	}
	var lines []parsedLine
	if err := json.Unmarshal([]byte(trimmed[start:end+1]), &lines); err != nil {
		return nil, fmt.Errorf("the answer does not parse as transactions: %v", err)
	}
	return lines, nil
}

func stageParsedLines(lines []parsedLine, meta uploadMeta, known map[string]bool) ([]stagedTransaction, []string) {
	var staged []stagedTransaction
	var problems []string
	for i, line := range lines {
		day, err := time.Parse("2006-01-02", line.Date)
		if err != nil {
			problems = append(problems, fmt.Sprintf("line %d: date %q is not YYYY-MM-DD", i+1, line.Date))
			continue
		}
		if strings.TrimSpace(line.Description) == "" {
			problems = append(problems, fmt.Sprintf("line %d: no description", i+1))
			continue
		}
		if line.Amount == 0 {
			problems = append(problems, fmt.Sprintf("line %d: amount is 0", i+1))
			continue
		}
		st := stagedTransaction{
			Date:        day.Format("2006-01-02"),
			Description: strings.TrimSpace(line.Description),
			Amount:      line.Amount,
			Account:     orDefault(line.Account, orDefault(meta.Account, "unknown")),
		}
		if known[line.Category] {
			st.Category = line.Category
		} else {
			st.Untracked = untrackedNote
		}
		staged = append(staged, st)
	}
	return staged, problems
}

func (s *server) handleUploadsStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uploadsSession(w, r); !ok {
		return
	}
	uploads, err := s.uploads.list()
	if err != nil {
		writeAPIError(w, err, http.StatusInternalServerError)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"uploads": uploads})
}

func (s *server) uploadFor(w http.ResponseWriter, r *http.Request) (uploadMeta, bool) {
	if _, ok := s.uploadsSession(w, r); !ok {
		return uploadMeta{}, false
	}
	meta, err := s.uploads.load(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return uploadMeta{}, false
	}
	return meta, true
}

func (s *server) handleUploadCategory(w http.ResponseWriter, r *http.Request) {
	meta, ok := s.uploadFor(w, r)
	if !ok {
		return
	}
	if s.uploadJobs.running(meta.ID) {
		http.Error(w, "that upload is still being parsed; wait for it to finish", http.StatusConflict)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	changed := 0
	for key, values := range r.Form {
		idx, err := strconv.Atoi(strings.TrimPrefix(key, "cat-"))
		if err != nil || !strings.HasPrefix(key, "cat-") || idx < 0 || idx >= len(meta.Transactions) || len(values) != 1 {
			continue
		}
		meta.Transactions[idx].Category, meta.Transactions[idx].Untracked = "", ""
		if values[0] == "untracked" {
			meta.Transactions[idx].Untracked = untrackedNote
		} else {
			meta.Transactions[idx].Category = values[0]
		}
		changed++
	}
	if changed == 0 {
		http.Error(w, "nothing to change", http.StatusBadRequest)
		return
	}
	if err := s.uploads.saveMeta(meta); err != nil {
		serverError(w, r, "saving categories", err)
		return
	}
	http.Redirect(w, r, "/uploads", http.StatusSeeOther)
}

func (s *server) handleUploadParse(w http.ResponseWriter, r *http.Request) {
	meta, ok := s.uploadFor(w, r)
	if !ok {
		return
	}
	if s.llm == nil {
		http.Error(w, "parsing is not configured (OPENAI_API_KEY is unset)", http.StatusServiceUnavailable)
		return
	}
	if s.uploadJobs.running(meta.ID) {
		http.Error(w, "that upload is already being parsed", http.StatusConflict)
		return
	}
	meta.Status, meta.Error = "pending", ""
	s.saveMetaOrLog(meta)
	s.uploadJobs.start(meta.ID)
	go s.parseUpload(meta)
	http.Redirect(w, r, "/uploads", http.StatusSeeOther)
}

func (s *server) handleUploadDelete(w http.ResponseWriter, r *http.Request) {
	meta, ok := s.uploadFor(w, r)
	if !ok {
		return
	}
	if s.uploadJobs.running(meta.ID) {
		http.Error(w, "that upload is still being parsed; wait for it to finish", http.StatusConflict)
		return
	}
	if err := s.uploads.delete(meta.ID); err != nil {
		serverError(w, r, "deleting upload", err)
		return
	}
	log.Printf("uploads: %s (%s) deleted", meta.ID, meta.Name)
	http.Redirect(w, r, "/uploads", http.StatusSeeOther)
}

func (s *server) handleUploadConfirm(w http.ResponseWriter, r *http.Request) {
	meta, ok := s.uploadFor(w, r)
	if !ok {
		return
	}
	if s.uploadJobs.running(meta.ID) {
		http.Error(w, "that upload is still being parsed; wait for it to finish", http.StatusConflict)
		return
	}
	if meta.Status != "parsed" {
		http.Error(w, "only parsed uploads can be confirmed", http.StatusConflict)
		return
	}
	svc := s.apiService()
	if svc.Store == nil {
		http.Error(w, "writes are not configured — set GITHUB_DATA_TOKEN to import transactions", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	categories, err := svc.Categories(ctx)
	if err != nil {
		serverError(w, r, "reading budget categories", err)
		return
	}
	known := map[string]bool{}
	for _, c := range categories {
		known[c.ID] = true
	}
	byMonth := map[string][]actualsdata.Transaction{}
	var order []string
	for _, t := range meta.Transactions {
		month := t.month()
		if _, seen := byMonth[month]; !seen {
			order = append(order, month)
		}
		byMonth[month] = append(byMonth[month], t.transaction())
	}
	sort.Strings(order)
	var importedTo []string
	for _, month := range order {
		path := svc.ActualsPath(month)
		content, sha, err := svc.Store.Get(ctx, path)
		if err != nil && !errors.Is(err, api.ErrNotFound) {
			serverError(w, r, "reading "+path, err)
			return
		}
		var af actualsdata.ActualsFile
		if errors.Is(err, api.ErrNotFound) {
			af = actualsdata.ActualsFile{Month: month}
			sha = ""
		} else if jerr := json.Unmarshal(content, &af); jerr != nil {
			serverError(w, r, "parsing "+path, jerr)
			return
		}
		existing := map[string]bool{}
		for _, tx := range af.Transactions {
			existing[tx.Id] = true
		}
		added := 0
		for _, tx := range byMonth[month] {
			id, duplicate := deriveTransactionID(tx, existing)
			if duplicate {
				continue
			}
			tx.Id = id
			existing[tx.Id] = true
			af.Transactions = append(af.Transactions, tx)
			added++
		}
		if added == 0 {
			importedTo = append(importedTo, month)
			continue
		}
		sortTransactions(af.Transactions)
		if err := actualsdata.ValidateActuals(af, month, known); err != nil {
			log.Printf("uploads: %s would not validate for %s: %v", meta.ID, month, err)
			http.Error(w, fmt.Sprintf("the merged %s file would not validate: %v", month, err), http.StatusBadRequest)
			return
		}
		body, err := marshalActuals(af)
		if err != nil {
			serverError(w, r, "serialising "+path, err)
			return
		}
		if _, err := svc.Store.Put(ctx, path, body, sha, fmt.Sprintf("actuals: import %d transaction(s) from %s", added, meta.Name)); err != nil {
			serverError(w, r, "committing "+path, err)
			return
		}
		log.Printf("uploads: %d transaction(s) imported into %s from %s", added, path, meta.Name)
		importedTo = append(importedTo, month)
	}
	meta.Status = importedStatus
	meta.Error = ""
	meta.ImportedTo = importedTo
	if err := s.uploads.saveMeta(meta); err != nil {
		serverError(w, r, "saving upload", err)
		return
	}
	http.Redirect(w, r, "/uploads", http.StatusSeeOther)
}

func marshalActuals(af actualsdata.ActualsFile) ([]byte, error) {
	schema := "../../internal/finance/data/actuals.schema.json"
	af.Schema = &schema
	body, err := json.MarshalIndent(af, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func sortTransactions(txs []actualsdata.Transaction) {
	sort.SliceStable(txs, func(i, j int) bool {
		if txs[i].Date != txs[j].Date {
			return txs[i].Date < txs[j].Date
		}
		return txs[i].Description < txs[j].Description
	})
}

func deriveTransactionID(tx actualsdata.Transaction, taken map[string]bool) (string, bool) {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%.2f|%s", tx.Account, tx.Date, tx.Amount, tx.Description)))
	base := hex.EncodeToString(sum[:])[:8]
	return base, taken[base]
}

func (s *server) handleUploadsPurge(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uploadsSession(w, r); !ok {
		return
	}
	n, err := s.uploads.purge()
	if err != nil {
		serverError(w, r, "deleting uploads", err)
		return
	}
	log.Printf("uploads: %d upload(s) deleted", n)
	http.Redirect(w, r, "/info", http.StatusSeeOther)
}
