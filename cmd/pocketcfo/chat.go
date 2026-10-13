package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/api"
	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	"github.com/titaniumcoder/pocket-cfo/internal/chat"
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
	Kind string
	Text string
	HTML template.HTML
	Logs []logLine
}

type logLine struct {
	Kind    string
	Name    string
	Text    string
	Content string
}

const logExcerpt = 240

func rowsOf(c *chat.Chat) []chatRow {
	var rows []chatRow
	var logs []logLine
	flushLogs := func() {
		if len(logs) > 0 {
			rows = append(rows, chatRow{Kind: "logs", Logs: logs})
			logs = nil
		}
	}
	for _, m := range c.Messages {
		switch m.Role {
		case "user":
			flushLogs()
			if strings.HasPrefix(m.Content, "System note: ") {
				rows = append(rows, chatRow{Kind: "note", Text: strings.TrimPrefix(m.Content, "System note: ")})
			} else {
				rows = append(rows, chatRow{Kind: "user", Text: m.Content})
			}
		case "assistant":
			if m.Reasoning != "" {
				logs = append(logs, logLine{Kind: "thinking", Text: m.Reasoning})
			}
			for _, tc := range m.ToolCalls {
				logs = append(logs, logLine{Kind: "call", Name: tc.Function.Name, Text: excerpt(tc.Function.Arguments), Content: tc.Function.Arguments})
			}
			if len(m.ToolCalls) == 0 && strings.TrimSpace(m.Content) != "" {
				flushLogs()
				rows = append(rows, chatRow{Kind: "answer", Text: m.Content, HTML: chat.Markdown(m.Content)})
			}
		case "tool":
			logs = append(logs, logLine{Kind: "result", Name: m.Name, Text: excerpt(m.Content), Content: m.Content})
		}
	}
	flushLogs()
	return rows
}

func excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > logExcerpt {
		return s[:logExcerpt] + "…"
	}
	return s
}

type chatView struct {
	Header  webui.Header
	Chat    *chat.Chat
	Rows    []chatRow
	Running bool
	Model   string
}

func (s *server) chatShow(w http.ResponseWriter, r *http.Request) {
	sess, c, ok := s.chatFor(w, r)
	if !ok {
		return
	}
	view := chatView{
		Header:  s.header(sess, webui.PageChat, webui.ParsePeriod(r.URL.Query().Get("year"), r.URL.Query().Get("month"))),
		Chat:    c,
		Rows:    rowsOf(c),
		Running: s.chatRuns.Active(c.ID),
		Model:   s.chatClient.Model(),
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
	if in.Empty() {
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
	runner := &chat.Runner{Client: s.chatClient, Store: s.chatStore, Service: s.apiService()}
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

func (s *server) saveChat(w http.ResponseWriter, r *http.Request, c *chat.Chat) bool {
	if err := s.chatStore.Save(c); err != nil {
		serverError(w, r, "saving chat", err)
		return false
	}
	return true
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
