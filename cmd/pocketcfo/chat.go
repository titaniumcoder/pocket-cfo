package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
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
	mux.HandleFunc("POST /chat/{id}/close", s.chatClose)
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
	Model    string
}

func rowsOf(c *chat.Chat) []chatRow {
	var rows []chatRow
	for _, m := range c.Messages {
		switch m.Role {
		case "user":
			rows = append(rows, chatRow{Kind: "user", Text: m.Content})
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
	unlock := s.chatStore.Lock(r.PathValue("id"))
	defer unlock()
	c, err := s.chatStore.Load(sess.Login, r.PathValue("id"))
	if errors.Is(err, chat.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, r, "loading chat", err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), chatTurnTimeout)
	defer cancel()
	http.NewResponseController(w).SetWriteDeadline(time.Now().Add(chatTurnTimeout))
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	stream := newEventStream(w)

	svc, _ := s.stagedService()
	runner := &chat.Runner{Client: s.chatClient, Store: s.chatStore, Service: svc}
	if err := runner.Run(ctx, c, in, stream.emit); err != nil {
		log.Printf("chat: %s turn by %s: %v", c.ID, sess.Login, err)
		stream.emit(chat.Event{Event: "error", Error: err.Error()})
	}
}

type eventStream struct {
	w   http.ResponseWriter
	enc *json.Encoder
}

func newEventStream(w http.ResponseWriter) *eventStream {
	return &eventStream{w: w, enc: json.NewEncoder(w)}
}

func (e *eventStream) emit(ev chat.Event) error {
	if err := e.enc.Encode(ev); err != nil {
		return err
	}
	if f, ok := e.w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
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
