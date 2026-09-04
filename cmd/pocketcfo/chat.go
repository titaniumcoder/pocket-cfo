package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/api"
	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	"github.com/titaniumcoder/pocket-cfo/internal/chat"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
	"github.com/titaniumcoder/pocket-cfo/internal/webui"
)

const (
	maxChatBody     = 8 << 20
	chatTurnTimeout = 10 * time.Minute
	chatHTTPTimeout = 200 * time.Second
)

func mustOpenChat(cfg config, shared *http.Client) (*chat.Store, *chat.Client) {
	store := &chat.Store{Dir: cfg.chatDir}
	if err := store.Open(); err != nil {
		log.Fatalf("pocketcfo: %v", err)
	}
	client, err := chat.NewClient(chat.ClientConfig{
		Key: cfg.openAIKey, BaseURL: cfg.openAIBaseURL, Model: cfg.openAIModel,
		ExtraBody: cfg.openAIExtraBody, Referer: cfg.baseURL, HTTP: chatHTTPClient(shared),
	})
	if err != nil {
		log.Fatalf("pocketcfo: %v", err)
	}
	log.Printf("pocketcfo: chat enabled — %s at %s, chats in %s", cfg.openAIModel, cfg.openAIBaseURL, cfg.chatDir)
	return store, client
}

func chatHTTPClient(shared *http.Client) *http.Client {
	c := *shared
	c.Timeout = chatHTTPTimeout
	return &c
}

func (s *server) registerChat(mux *http.ServeMux) {
	if !s.cfg.chatEnabled() || s.chatStore == nil {
		mux.HandleFunc("GET /chat", http.NotFound)
		mux.HandleFunc("GET /chat/{id}", http.NotFound)
		return
	}
	mux.HandleFunc("GET /chat", s.chatList)
	mux.HandleFunc("POST /chat/new", s.chatNew)
	mux.HandleFunc("GET /chat/{id}", s.chatShow)
	mux.HandleFunc("POST /chat/{id}/turn", s.chatTurn)
	mux.HandleFunc("GET /chat/{id}/events", s.chatEvents)
	mux.HandleFunc("POST /chat/{id}/close", s.chatClose)
	mux.HandleFunc("POST /chat/{id}/apply", s.chatApply)
	mux.HandleFunc("POST /chat/{id}/discard", s.chatDiscard)
	mux.HandleFunc("POST /chat/{id}/revert", s.chatRevert)
	mux.HandleFunc("POST /chat/purge", s.chatPurge)
}

func (s *server) chatSession(w http.ResponseWriter, r *http.Request) (auth.Session, bool) {
	sess, ok := s.currentSession(r)
	if !ok || !s.authenticated(sess) {
		s.rememberDestination(w, r)
		http.Redirect(w, r, "/auth/login", http.StatusFound)
		return sess, false
	}
	if !s.admin(sess) {
		http.Error(w, "the chat is for administrators only", http.StatusForbidden)
		return sess, false
	}
	return sess, true
}

func (s *server) chatFor(w http.ResponseWriter, r *http.Request) (auth.Session, *chat.Chat, bool) {
	sess, ok := s.chatSession(w, r)
	if !ok {
		return sess, nil, false
	}
	c, err := s.chatStore.Load(sess.Login, r.PathValue("id"))
	if errors.Is(err, chat.ErrNotFound) {
		http.NotFound(w, r)
		return sess, nil, false
	}
	if err != nil {
		serverError(w, r, "loading chat", err)
		return sess, nil, false
	}
	return sess, c, true
}

type chatsView struct {
	Header webui.Header
	Chats  []chat.Summary
	Model  string
}

func (s *server) chatList(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.chatSession(w, r)
	if !ok {
		return
	}
	chats, err := s.chatStore.List(sess.Login)
	if err != nil {
		serverError(w, r, "listing chats", err)
		return
	}
	view := chatsView{
		Header: s.header(sess, webui.PageChat, webui.ParsePeriod(r.URL.Query().Get("year"), r.URL.Query().Get("month"))),
		Chats:  chats,
		Model:  s.chatClient.Model(),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.chatsTmpl.Execute(w, view); err != nil {
		serverError(w, r, "rendering chats", err)
	}
}

func (s *server) chatNew(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.chatSession(w, r)
	if !ok {
		return
	}
	c, err := s.chatStore.Create(sess.Login)
	if err != nil {
		serverError(w, r, "creating chat", err)
		return
	}
	http.Redirect(w, r, "/chat/"+c.ID, http.StatusSeeOther)
}

type chatRow struct {
	Kind    string
	Text    string
	Calls   []string
	Tool    string
	Content string
}

type chatView struct {
	Header   webui.Header
	Chat     *chat.Chat
	Rows     []chatRow
	Writable bool
	Running  bool
	Model    string
}

func rowsOf(c *chat.Chat) []chatRow {
	var rows []chatRow
	for _, m := range c.Messages {
		switch m.Role {
		case "user":
			kind := "user"
			if strings.HasPrefix(m.Content, "System note: ") {
				kind = "note"
			}
			rows = append(rows, chatRow{Kind: kind, Text: m.Content})
		case "assistant":
			row := chatRow{Kind: "assistant", Text: m.Content}
			for _, tc := range m.ToolCalls {
				row.Calls = append(row.Calls, tc.Function.Name+" "+tc.Function.Arguments)
			}
			rows = append(rows, row)
		case "tool":
			rows = append(rows, chatRow{Kind: "tool", Tool: m.Name, Content: m.Content})
		}
	}
	return rows
}

func (s *server) chatShow(w http.ResponseWriter, r *http.Request) {
	sess, c, ok := s.chatFor(w, r)
	if !ok {
		return
	}
	view := chatView{
		Header:   s.header(sess, webui.PageChat, webui.ParsePeriod(r.URL.Query().Get("year"), r.URL.Query().Get("month"))),
		Chat:     c,
		Rows:     rowsOf(c),
		Writable: s.cfg.githubDataToken != "",
		Running:  s.chatRuns.Active(c.ID),
		Model:    s.chatClient.Model(),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.chatTmpl.Execute(w, view); err != nil {
		serverError(w, r, "rendering chat", err)
	}
}

func (s *server) chatClose(w http.ResponseWriter, r *http.Request) {
	sess, c, ok := s.chatFor(w, r)
	if !ok {
		return
	}
	if s.chatRuns.Active(c.ID) {
		http.Error(w, "a turn is still running; wait for it to finish", http.StatusConflict)
		return
	}
	unlock := s.chatStore.Lock(c.ID)
	defer unlock()
	if err := s.chatStore.Delete(sess.Login, c.ID); err != nil {
		serverError(w, r, "closing chat", err)
		return
	}
	log.Printf("chat: %s closed by %s", c.ID, sess.Login)
	http.Redirect(w, r, "/chat", http.StatusSeeOther)
}

func decodeChatJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChatBody))
	if err := dec.Decode(into); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "that is more than the chat accepts in one turn", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func (s *server) chatTurn(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.chatSession(w, r)
	if !ok {
		return
	}
	var in chat.Input
	if !decodeChatJSON(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Text) == "" && len(in.Files) == 0 {
		http.Error(w, chat.ErrEmptyTurn.Error(), http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	if s.chatRuns.Active(id) {
		writeAPIError(w, &api.Error{Code: api.CodeConflict, Message: "a turn is already running"}, http.StatusConflict)
		return
	}
	unlock := s.chatStore.Lock(id)
	c, err := s.chatStore.Load(sess.Login, id)
	if errors.Is(err, chat.ErrNotFound) {
		unlock()
		http.NotFound(w, r)
		return
	}
	if err != nil {
		unlock()
		serverError(w, r, "loading chat", err)
		return
	}
	svc, _ := s.stagedService()
	runner := &chat.Runner{Client: s.chatClient, Store: s.chatStore, Service: svc}
	if _, started := s.chatRuns.Start(c, in, runner, unlock); !started {
		unlock()
		writeAPIError(w, &api.Error{Code: api.CodeConflict, Message: "a turn is already running"}, http.StatusConflict)
		return
	}
	log.Printf("chat: %s turn started by %s", c.ID, sess.Login)
	writeAPIJSON(w, http.StatusAccepted, map[string]any{"running": true})
}

func (s *server) chatEvents(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.chatSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if _, err := s.chatStore.Load(sess.Login, id); err != nil {
		http.NotFound(w, r)
		return
	}
	run, ok := s.chatRuns.Get(id)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	if since < 0 {
		since = 0
	}
	http.NewResponseController(w).SetWriteDeadline(time.Now().Add(chatTurnTimeout))
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for {
		events, done := run.Next(r.Context(), since)
		for _, ev := range events {
			body, err := json.Marshal(ev)
			if err != nil {
				return
			}
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", since, body)
			since++
		}
		if flusher != nil {
			flusher.Flush()
		}
		if done || r.Context().Err() != nil {
			return
		}
	}
}

const chatApplyTimeout = 2 * time.Minute

type indexRequest struct {
	Index int `json:"index"`
}

func (s *server) lockedChat(w http.ResponseWriter, r *http.Request) (auth.Session, *chat.Chat, func(), bool) {
	sess, ok := s.chatSession(w, r)
	if !ok {
		return sess, nil, nil, false
	}
	if s.chatRuns.Active(r.PathValue("id")) {
		writeAPIError(w, &api.Error{Code: api.CodeConflict, Message: "a turn is still running; wait for it to finish"}, http.StatusConflict)
		return sess, nil, nil, false
	}
	unlock := s.chatStore.Lock(r.PathValue("id"))
	c, err := s.chatStore.Load(sess.Login, r.PathValue("id"))
	if errors.Is(err, chat.ErrNotFound) {
		unlock()
		http.NotFound(w, r)
		return sess, nil, nil, false
	}
	if err != nil {
		unlock()
		serverError(w, r, "loading chat", err)
		return sess, nil, nil, false
	}
	return sess, c, unlock, true
}

func (s *server) chatApply(w http.ResponseWriter, r *http.Request) {
	sess, c, unlock, ok := s.lockedChat(w, r)
	if !ok {
		return
	}
	defer unlock()
	if len(c.Pending) == 0 {
		writeAPIError(w, &api.Error{Code: api.CodeInvalidRequest, Message: "nothing is waiting for approval"}, http.StatusBadRequest)
		return
	}
	svc, st := s.stagedService()
	if st == nil {
		writeAPIError(w, &api.Error{Code: api.CodeWriteNotConfigured, Message: "writes are not configured"}, http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), chatApplyTimeout)
	defer cancel()

	calls := make([]api.ToolCall, 0, len(c.Pending))
	for _, p := range c.Pending {
		calls = append(calls, api.ToolCall{Name: p.Tool, Arguments: p.Arguments})
	}
	failed := false
	for i, staged := range api.Replay(ctx, svc, calls) {
		c.Pending[i].Summary, c.Pending[i].Error = summaryOf(staged), errorOf(staged)
		failed = failed || staged.Err != nil
	}
	if failed {
		s.saveChat(w, r, c)
		writeAPIError(w, &api.Error{Code: api.CodeValidationFailed, Message: "a pending change no longer applies — discard it or ask the chat to redo it", Details: c.Pending}, http.StatusConflict)
		return
	}

	live := s.apiService()
	contents := map[string][]byte{}
	for _, f := range st.Files() {
		contents[f.Path] = f.Content
	}
	receipts, err := st.Flush(ctx, live.Store, sess.Login)
	for _, rc := range receipts {
		live.Publish(rc.Path, contents[rc.Path])
		c.Applied = append(c.Applied, applied(rc))
	}
	if err != nil {
		log.Printf("chat: %s apply by %s: %v", c.ID, sess.Login, err)
		s.saveChat(w, r, c)
		writeAPIError(w, err, apiStatus(err))
		return
	}
	c.Pending = nil
	c.Messages = append(c.Messages, chat.Message{Role: "user", Content: noteOf("approved and committed", receipts)})
	log.Printf("chat: %s applied %d change(s) by %s", c.ID, len(receipts), sess.Login)
	if !s.saveChat(w, r, c) {
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"applied": receipts})
}

func applied(rc api.Receipt) chat.AppliedChange {
	return chat.AppliedChange{
		Path: rc.Path, Message: strings.SplitN(rc.Message, "\n", 2)[0],
		BeforeSHA: rc.BeforeSHA, AfterSHA: rc.AfterSHA, Created: rc.Created, At: time.Now(),
	}
}

func noteOf(what string, receipts []api.Receipt) string {
	var paths []string
	for _, rc := range receipts {
		paths = append(paths, rc.Path)
	}
	return "System note: the user " + what + " " + strings.Join(paths, ", ") + ". Nothing is pending now; the data reflects it."
}

func summaryOf(staged api.Staged) string {
	if staged.Result == nil {
		return ""
	}
	b, err := json.Marshal(staged.Result)
	if err != nil {
		return ""
	}
	return string(b)
}

func errorOf(staged api.Staged) string {
	if staged.Err == nil {
		return ""
	}
	return staged.Err.Message
}

func (s *server) saveChat(w http.ResponseWriter, r *http.Request, c *chat.Chat) bool {
	if err := s.chatStore.Save(c); err != nil {
		serverError(w, r, "saving chat", err)
		return false
	}
	return true
}

func (s *server) chatDiscard(w http.ResponseWriter, r *http.Request) {
	var req indexRequest
	if !decodeChatJSON(w, r, &req) {
		return
	}
	sess, c, unlock, ok := s.lockedChat(w, r)
	if !ok {
		return
	}
	defer unlock()
	if req.Index < 0 || req.Index >= len(c.Pending) {
		writeAPIError(w, &api.Error{Code: api.CodeNotFound, Message: "no such pending change"}, http.StatusNotFound)
		return
	}
	dropped := c.Pending[req.Index]
	c.Pending = append(c.Pending[:req.Index:req.Index], c.Pending[req.Index+1:]...)
	c.Messages = append(c.Messages, chat.Message{Role: "user", Content: "System note: the user discarded the pending " + dropped.Tool + " change; do not stage it again unless asked."})
	log.Printf("chat: %s discarded a pending %s by %s", c.ID, dropped.Tool, sess.Login)
	if !s.saveChat(w, r, c) {
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"pending": len(c.Pending)})
}

func (s *server) chatRevert(w http.ResponseWriter, r *http.Request) {
	var req indexRequest
	if !decodeChatJSON(w, r, &req) {
		return
	}
	sess, c, unlock, ok := s.lockedChat(w, r)
	if !ok {
		return
	}
	defer unlock()
	if req.Index < 0 || req.Index >= len(c.Applied) {
		writeAPIError(w, &api.Error{Code: api.CodeNotFound, Message: "no such applied change"}, http.StatusNotFound)
		return
	}
	a := c.Applied[req.Index]
	if a.Reverted {
		writeAPIError(w, &api.Error{Code: api.CodeConflict, Message: "already reverted"}, http.StatusConflict)
		return
	}
	live := s.apiService()
	rev, ok := live.Store.(api.Reverter)
	if !ok {
		writeAPIError(w, &api.Error{Code: api.CodeWriteNotConfigured, Message: "writes are not configured"}, http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), chatApplyTimeout)
	defer cancel()
	receipt := api.Receipt{Path: a.Path, Message: a.Message, BeforeSHA: a.BeforeSHA, AfterSHA: a.AfterSHA, Created: a.Created}
	body, err := api.Revert(ctx, live.Store, rev, receipt)
	if err != nil {
		log.Printf("chat: %s revert of %s by %s: %v", c.ID, a.Path, sess.Login, err)
		writeAPIError(w, err, apiStatus(err))
		return
	}
	if body == nil {
		live.Unpublish(a.Path)
	} else {
		live.Publish(a.Path, body)
	}
	c.Applied[req.Index].Reverted = true
	c.Messages = append(c.Messages, chat.Message{Role: "user", Content: "System note: the user reverted the commit to " + a.Path + " (" + a.Message + "); the data is back to what it was before it."})
	log.Printf("chat: %s reverted %s by %s", c.ID, a.Path, sess.Login)
	if !s.saveChat(w, r, c) {
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"reverted": a.Path})
}

func (s *server) chatPurge(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.chatSession(w, r)
	if !ok {
		return
	}
	n, err := s.chatStore.Purge()
	if err != nil {
		serverError(w, r, "deleting chats", err)
		return
	}
	log.Printf("chat: %d chat(s) deleted by %s", n, sess.Login)
	http.Redirect(w, r, "/info", http.StatusSeeOther)
}

func (s *server) stagedService() (*api.Service, *api.Staging) {
	svc := s.apiService()
	if s.tracker != nil {
		trk, err := s.trackerForRequest()
		if err != nil {
			log.Printf("chat: invoiced facts unavailable: %v", err)
		}
		budget := &tracker.Budget{FS: s.tracker.Budget.FS}
		accounts := &tracker.Accounts{FS: s.tracker.Accounts.FS}
		actuals := &tracker.Actuals{FS: s.tracker.Actuals.FS}
		trk.Budget, trk.Accounts, trk.Actuals = budget, accounts, actuals
		svc.Budget, svc.Accounts, svc.Actuals = budget, accounts, actuals
		svc.Figures = func(ctx context.Context, year int, month time.Month) (tracker.Figures, error) {
			return trk.ComputeMonth(ctx, year, month), nil
		}
	}
	if svc.Store == nil {
		return svc, nil
	}
	st := api.NewStaging(svc.Store)
	svc.Store = st
	return svc, st
}
