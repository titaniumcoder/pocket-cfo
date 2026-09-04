package api

import (
	"context"
	"encoding/json"
	"path"
	"sort"
	"strings"
	"sync"
)

const stagedSHA = "staged"

type ToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Staged struct {
	Call   ToolCall
	Result any
	Err    *Error
}

type PendingFile struct {
	Path     string
	Content  []byte
	BaseSHA  string
	Messages []string
	Created  bool
}

type Receipt struct {
	Path      string `json:"path"`
	Message   string `json:"message"`
	BeforeSHA string `json:"before_sha,omitempty"`
	AfterSHA  string `json:"after_sha"`
	Created   bool   `json:"created,omitempty"`
}

type Staging struct {
	base  Store
	mu    sync.Mutex
	files map[string]*PendingFile
	order []string
}

func NewStaging(base Store) *Staging {
	return &Staging{base: base, files: map[string]*PendingFile{}}
}

func (st *Staging) Get(ctx context.Context, p string) ([]byte, string, error) {
	st.mu.Lock()
	f, ok := st.files[p]
	st.mu.Unlock()
	if ok {
		return f.Content, stagedSHA, nil
	}
	return st.base.Get(ctx, p)
}

func (st *Staging) Put(_ context.Context, p string, content []byte, baseSHA, message string) (string, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	f, ok := st.files[p]
	if !ok {
		f = &PendingFile{Path: p, BaseSHA: baseSHA, Created: baseSHA == ""}
		st.files[p] = f
		st.order = append(st.order, p)
	}
	f.Content = content
	f.Messages = append(f.Messages, strings.TrimSpace(message))
	return stagedSHA, nil
}

func (st *Staging) List(ctx context.Context, dir string) ([]string, error) {
	names, err := st.base.List(ctx, dir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for p := range st.files {
		if path.Dir(p) == strings.TrimSuffix(dir, "/") && !seen[path.Base(p)] {
			names = append(names, path.Base(p))
		}
	}
	sort.Strings(names)
	return names, nil
}

func (st *Staging) Files() []PendingFile {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]PendingFile, 0, len(st.order))
	for _, p := range st.order {
		out = append(out, *st.files[p])
	}
	return out
}

func Replay(ctx context.Context, svc *Service, calls []ToolCall) []Staged {
	byName := map[string]Tool{}
	for _, t := range svc.Tools() {
		byName[t.Name] = t
	}
	out := make([]Staged, 0, len(calls))
	for _, c := range calls {
		s := Staged{Call: c}
		t, ok := byName[c.Name]
		switch {
		case !ok:
			s.Err = errorf(CodeInvalidRequest, "no tool named %q", c.Name)
		case t.ReadOnly:
			s.Err = errorf(CodeInvalidRequest, "%s does not change anything, so it cannot be pending", c.Name)
		default:
			s.Result, s.Err = asError(t.Call(ctx, c.Arguments))
		}
		out = append(out, s)
	}
	return out
}

func asError(v any, err error) (any, *Error) {
	if err == nil {
		return v, nil
	}
	if e, ok := err.(*Error); ok {
		return nil, e
	}
	return nil, &Error{Code: CodeInternal, Message: err.Error()}
}

func (st *Staging) Flush(ctx context.Context, base Store, approvedBy string) ([]Receipt, error) {
	var receipts []Receipt
	for _, f := range st.Files() {
		message := commitMessage(f.Messages, approvedBy)
		sha, err := base.Put(ctx, f.Path, f.Content, f.BaseSHA, message)
		if err != nil {
			return receipts, flushError(err, f.Path, receipts)
		}
		receipts = append(receipts, Receipt{Path: f.Path, Message: message, BeforeSHA: f.BaseSHA, AfterSHA: sha, Created: f.Created})
	}
	return receipts, nil
}

func commitMessage(messages []string, approvedBy string) string {
	subject := messages[0]
	body := ""
	if len(messages) > 1 {
		subject = strings.SplitN(messages[0], "\n", 2)[0] + " (+" + countLabel(len(messages)-1, "more change") + ")"
		body = "\n\n- " + strings.Join(messages, "\n- ")
	}
	return subject + body + "\n\nApproved in the chat by " + approvedBy + ".\n"
}

func flushError(err error, p string, done []Receipt) error {
	details := map[string]any{"failed_path": p}
	if len(done) > 0 {
		var landed []string
		for _, r := range done {
			landed = append(landed, r.Path)
		}
		details["already_committed"] = landed
	}
	if e, ok := err.(*Error); ok {
		return &Error{Code: e.Code, Message: e.Message, Details: details}
	}
	return &Error{Code: CodeUpstream, Message: "committing " + p + ": " + err.Error(), Details: details}
}

type Reverter interface {
	Blob(ctx context.Context, sha string) ([]byte, error)
	Delete(ctx context.Context, path, sha, message string) error
}

func Revert(ctx context.Context, store Store, rev Reverter, r Receipt) ([]byte, error) {
	_, current, err := store.Get(ctx, r.Path)
	if err != nil && err != ErrNotFound {
		return nil, errorf(CodeUpstream, "reading %s: %v", r.Path, err)
	}
	if current != r.AfterSHA {
		return nil, errorf(CodeConflict, "%s has changed since this change was applied, so it cannot be reverted from here — repair it in the data repo", r.Path)
	}
	message := "revert: " + strings.SplitN(r.Message, "\n", 2)[0] + "\n"
	if r.Created {
		if err := rev.Delete(ctx, r.Path, r.AfterSHA, message); err != nil {
			return nil, errorf(CodeUpstream, "removing %s: %v", r.Path, err)
		}
		return nil, nil
	}
	before, err := rev.Blob(ctx, r.BeforeSHA)
	if err != nil {
		return nil, errorf(CodeUpstream, "reading the previous %s: %v", r.Path, err)
	}
	if _, err := store.Put(ctx, r.Path, before, r.AfterSHA, message); err != nil {
		return nil, err
	}
	return before, nil
}

func (s *Service) Publish(p string, body []byte) {
	switch {
	case p == s.budgetPath():
		s.Budget.Publish(body)
	case p == s.accountsPath():
		s.Accounts.Publish(body)
	case s.isActualsPath(p):
		s.Actuals.Publish(monthFileKey(p), body)
	}
}

func (s *Service) Unpublish(p string) {
	if s.isActualsPath(p) {
		s.Actuals.Unpublish(monthFileKey(p))
	}
}

func (s *Service) isActualsPath(p string) bool {
	return path.Dir(p) == path.Dir(s.actualsPath("0000-00")) && strings.HasSuffix(p, ".json")
}

func monthFileKey(p string) string {
	return strings.TrimSuffix(path.Base(p), ".json")
}
