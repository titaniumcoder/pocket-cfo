package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	"github.com/titaniumcoder/pocket-cfo/internal/chat"
	"github.com/titaniumcoder/pocket-cfo/internal/webui"
)

func chatServer(t *testing.T, answers ...string) *server {
	t.Helper()
	return enableChat(t, apiServer(t, apiTestToken, "prod"), answers...)
}

func enableChat(t *testing.T, s *server, answers ...string) *server {
	t.Helper()
	s.cfg.openAIKey, s.cfg.openAIModel, s.cfg.chatDir = "sk-test", "acme/model", filepath.Join(t.TempDir(), "chats")
	n := 0
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if n < len(answers) {
			io.WriteString(w, answers[n])
		} else {
			io.WriteString(w, answers[len(answers)-1])
		}
		n++
	}))
	t.Cleanup(stub.Close)
	s.cfg.openAIBaseURL = stub.URL
	s.chatsTmpl = mustPageTemplate("../../templates/chats.html")
	s.chatTmpl = mustPageTemplate("../../templates/chat.html")
	s.chatStore, s.chatClient = mustOpenChat(s.cfg, &http.Client{Timeout: 5 * time.Second})
	s.chatRuns = chat.NewRuns()
	return s
}

func sessionRequest(t *testing.T, s *server, method, path, permission string, body string, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
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

func TestChatDoesNotExistWithoutAKey(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")
	if w := sessionRequest(t, s, http.MethodGet, "/chat", "admin", "", ""); w.Code != http.StatusNotFound {
		t.Errorf("/chat without a key = %d", w.Code)
	}
	if s.header(auth.NewSession("octocat", "admin", time.Hour), "info", webui.Period{}).ShowChat {
		t.Error("the menu must not offer a chat that does not exist")
	}
}

func TestChatIsForAdminsOnly(t *testing.T) {
	s := chatServer(t, textAnswer("hello"))
	for _, tc := range []struct {
		permission string
		want       int
	}{{"admin", http.StatusOK}, {"push", http.StatusForbidden}, {"readonly", http.StatusForbidden}, {"", http.StatusFound}} {
		if w := sessionRequest(t, s, http.MethodGet, "/chat", tc.permission, "", ""); w.Code != tc.want {
			t.Errorf("%q: /chat = %d, want %d", tc.permission, w.Code, tc.want)
		}
	}
	if !s.header(auth.NewSession("octocat", "admin", time.Hour), "info", webui.Period{}).ShowChat {
		t.Error("an admin's menu must offer the chat")
	}
	if s.header(auth.NewSession("octocat", "push", time.Hour), "info", webui.Period{}).ShowChat {
		t.Error("a push collaborator's menu must not offer the chat")
	}
}

func textAnswer(text string) string {
	return `{"id":"c","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"` + text + `"}}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`
}

func TestATurnRunsInTheBackgroundAndTheEventsStreamReplaysIt(t *testing.T) {
	s := chatServer(t, textAnswer("Hello from the model"))

	w := sessionRequest(t, s, http.MethodPost, "/chat/new", "admin", "", "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("new = %d", w.Code)
	}
	location := w.Header().Get("Location")
	if !strings.HasPrefix(location, "/chat/") {
		t.Fatalf("location = %q", location)
	}

	w = sessionRequest(t, s, http.MethodPost, location+"/turn", "admin", `{"text":"hi","files":[{"name":"a.csv","content":"x;y\n"}]}`, "application/json")
	if w.Code != http.StatusAccepted {
		t.Fatalf("turn = %d %s", w.Code, w.Body)
	}

	w = sessionRequest(t, s, http.MethodGet, location+"/events", "admin", "", "")
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events = %d %s %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	stream := w.Body.String()
	if !strings.Contains(stream, `"event":"assistant"`) || !strings.Contains(stream, `"event":"done"`) || !strings.Contains(stream, "id: 0\n") {
		t.Fatalf("stream = %q", stream)
	}
	w = sessionRequest(t, s, http.MethodGet, location+"/events?since=2", "admin", "", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "data:") {
		t.Errorf("a subscriber past the end gets nothing more: %d %q", w.Code, w.Body.String())
	}

	w = sessionRequest(t, s, http.MethodGet, location, "admin", "", "")
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "Hello from the model") || !strings.Contains(body, "Attached file `a.csv`") {
		t.Errorf("chat page = %d:\n%s", w.Code, body)
	}
	if !strings.Contains(body, `<title>Pocket CFO — hi</title>`) {
		t.Error("the first line names the chat")
	}
	if strings.Contains(body, `data-running="true"`) {
		t.Error("a finished turn must not leave the composer disabled")
	}
	w = sessionRequest(t, s, http.MethodGet, "/chat", "admin", "", "")
	if !strings.Contains(w.Body.String(), `href="`+location+`"`) {
		t.Error("the list must link the chat")
	}
}

func TestWhileATurnRunsThePageSaysSoAndChangesWait(t *testing.T) {
	s := chatServer(t, textAnswer("x"))
	c, _ := s.chatStore.Create("octocat")
	r, started := s.chatRuns.Start(c, chat.Input{Text: "slow"}, slowChatRunner(t, s), func() {})
	if !started {
		t.Fatal("run must start")
	}
	page := sessionRequest(t, s, http.MethodGet, "/chat/"+c.ID, "admin", "", "").Body.String()
	if !strings.Contains(page, `data-running="true"`) || !strings.Contains(page, "Thinking…") {
		t.Error("the page must show the running turn and disable the composer")
	}
	for _, path := range []string{"/apply", "/discard", "/revert"} {
		if w := sessionRequest(t, s, http.MethodPost, "/chat/"+c.ID+path, "admin", `{"index":0}`, "application/json"); w.Code != http.StatusConflict {
			t.Errorf("%s during a run = %d", path, w.Code)
		}
	}
	if w := sessionRequest(t, s, http.MethodPost, "/chat/"+c.ID+"/turn", "admin", `{"text":"again"}`, "application/json"); w.Code != http.StatusConflict {
		t.Errorf("a second turn during a run = %d", w.Code)
	}
	if w := sessionRequest(t, s, http.MethodPost, "/chat/"+c.ID+"/close", "admin", "", "application/x-www-form-urlencoded"); w.Code != http.StatusConflict {
		t.Errorf("close during a run = %d", w.Code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !r.Done() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}

func slowChatRunner(t *testing.T, s *server) *chat.Runner {
	t.Helper()
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, textAnswer("slow answer"))
	}))
	t.Cleanup(stub.Close)
	client, err := chat.NewClient(chat.ClientConfig{Key: s.cfg.openAIKey, BaseURL: stub.URL, Model: s.cfg.openAIModel, HTTP: &http.Client{Timeout: 5 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := s.stagedService()
	return &chat.Runner{Client: client, Store: s.chatStore, Service: svc}
}

func TestATurnNeedsJSONAndSomethingToSay(t *testing.T) {
	s := chatServer(t, textAnswer("x"))
	c, _ := s.chatStore.Create("octocat")
	if w := sessionRequest(t, s, http.MethodPost, "/chat/"+c.ID+"/turn", "admin", "text=hi", "application/x-www-form-urlencoded"); w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form post = %d", w.Code)
	}
	if w := sessionRequest(t, s, http.MethodPost, "/chat/"+c.ID+"/turn", "admin", `{"text":" "}`, "application/json"); w.Code != http.StatusBadRequest {
		t.Errorf("empty turn = %d", w.Code)
	}
}

func TestAnotherUsersChatIsNotFoundAndCloseDeletes(t *testing.T) {
	s := chatServer(t, textAnswer("x"))
	c, _ := s.chatStore.Create("someone-else")
	if w := sessionRequest(t, s, http.MethodGet, "/chat/"+c.ID, "admin", "", ""); w.Code != http.StatusNotFound {
		t.Errorf("another user's chat = %d", w.Code)
	}
	mine, _ := s.chatStore.Create("octocat")
	if w := sessionRequest(t, s, http.MethodPost, "/chat/"+mine.ID+"/close", "admin", "", "application/x-www-form-urlencoded"); w.Code != http.StatusSeeOther {
		t.Errorf("close = %d", w.Code)
	}
	if _, err := s.chatStore.Load("octocat", mine.ID); err != chat.ErrNotFound {
		t.Errorf("closed chat still there: %v", err)
	}
}

const pendingAdd = `{"transactions":[{"id":"n1","date":"2026-08-20","description":"NEW LINE","amount":12.5,"account":"Private Checking","category":"00000000-0000-4000-8000-000000000001"}]}`

func chatWithPending(t *testing.T) (*server, *fakeContents, *chat.Chat) {
	t.Helper()
	s, gh := writingServer(t, map[string]string{"data/actuals/2026-08.json": apiActualsJSON})
	enableChat(t, s, textAnswer("ok"))
	c, _ := s.chatStore.Create("octocat")
	c.Pending = []chat.PendingChange{
		{Tool: "add_transactions", Arguments: json.RawMessage(pendingAdd)},
		{Tool: "edit_transactions", Arguments: json.RawMessage(`{"edits":[{"id":"n1","month":"2026-08","ignored":"actually a transfer"}],"reason":"transfer"}`)},
	}
	if err := s.chatStore.Save(c); err != nil {
		t.Fatal(err)
	}
	return s, gh, c
}

func TestApplyCommitsOncePerFileAndRevertPutsItBack(t *testing.T) {
	s, gh, c := chatWithPending(t)
	before := string(gh.files["data/actuals/2026-08.json"])

	w := sessionRequest(t, s, http.MethodPost, "/chat/"+c.ID+"/apply", "admin", `{}`, "application/json")
	if w.Code != http.StatusOK {
		t.Fatalf("apply = %d %s", w.Code, w.Body)
	}
	after := string(gh.files["data/actuals/2026-08.json"])
	if gh.puts != 1 || !strings.Contains(after, "NEW LINE") || !strings.Contains(after, "actually a transfer") {
		t.Errorf("want one commit carrying both changes, got %d puts:\n%s", gh.puts, after)
	}
	saved, _ := s.chatStore.Load("octocat", c.ID)
	if len(saved.Pending) != 0 || len(saved.Applied) != 1 || saved.Applied[0].Path != "data/actuals/2026-08.json" || saved.Applied[0].Reverted {
		t.Fatalf("saved = pending %+v applied %+v", saved.Pending, saved.Applied)
	}
	if !strings.Contains(saved.Applied[0].Message, "+1 more change") {
		t.Errorf("message = %q", saved.Applied[0].Message)
	}
	last := saved.Messages[len(saved.Messages)-1]
	if !strings.HasPrefix(last.Content, "System note: the user approved") {
		t.Errorf("the model must learn of the approval: %+v", last)
	}
	page := sessionRequest(t, s, http.MethodGet, "/chat/"+c.ID, "admin", "", "").Body.String()
	if !strings.Contains(page, `data-action="revert"`) || strings.Contains(page, `data-action="apply"`) {
		t.Error("the page must offer Revert and no longer Approve")
	}

	w = sessionRequest(t, s, http.MethodPost, "/chat/"+c.ID+"/revert", "admin", `{"index":0}`, "application/json")
	if w.Code != http.StatusOK {
		t.Fatalf("revert = %d %s", w.Code, w.Body)
	}
	if string(gh.files["data/actuals/2026-08.json"]) != before {
		t.Errorf("revert did not restore the month:\n%s", gh.files["data/actuals/2026-08.json"])
	}
	saved, _ = s.chatStore.Load("octocat", c.ID)
	if !saved.Applied[0].Reverted {
		t.Error("the applied change must be marked reverted")
	}
	if w := sessionRequest(t, s, http.MethodPost, "/chat/"+c.ID+"/revert", "admin", `{"index":0}`, "application/json"); w.Code != http.StatusConflict {
		t.Errorf("a second revert = %d", w.Code)
	}
}

func TestApplyRefusesWhenAPendingChangeNoLongerApplies(t *testing.T) {
	s, gh, c := chatWithPending(t)
	c.Pending = append(c.Pending, chat.PendingChange{Tool: "add_transactions", Arguments: json.RawMessage(`{"transactions":[{"id":"bad","date":"2026-08-21","description":"X","amount":1,"account":"Private Checking","category":"nope"}]}`)})
	s.chatStore.Save(c)
	w := sessionRequest(t, s, http.MethodPost, "/chat/"+c.ID+"/apply", "admin", `{}`, "application/json")
	if w.Code != http.StatusConflict || gh.puts != 0 {
		t.Fatalf("apply = %d with %d puts: %s", w.Code, gh.puts, w.Body)
	}
	saved, _ := s.chatStore.Load("octocat", c.ID)
	if len(saved.Pending) != 3 || saved.Pending[2].Error == "" || saved.Pending[0].Error != "" {
		t.Errorf("pending = %+v", saved.Pending)
	}
}

func TestDiscardDropsOnePendingChange(t *testing.T) {
	s, gh, c := chatWithPending(t)
	w := sessionRequest(t, s, http.MethodPost, "/chat/"+c.ID+"/discard", "admin", `{"index":1}`, "application/json")
	if w.Code != http.StatusOK {
		t.Fatalf("discard = %d %s", w.Code, w.Body)
	}
	saved, _ := s.chatStore.Load("octocat", c.ID)
	if len(saved.Pending) != 1 || saved.Pending[0].Tool != "add_transactions" || gh.puts != 0 {
		t.Errorf("pending = %+v, puts %d", saved.Pending, gh.puts)
	}
	if w := sessionRequest(t, s, http.MethodPost, "/chat/"+c.ID+"/discard", "admin", `{"index":5}`, "application/json"); w.Code != http.StatusNotFound {
		t.Errorf("out of range = %d", w.Code)
	}
}

func TestApplyWithoutWritesConfiguredIs503(t *testing.T) {
	s := chatServer(t, textAnswer("x"))
	c, _ := s.chatStore.Create("octocat")
	c.Pending = []chat.PendingChange{{Tool: "add_transactions", Arguments: json.RawMessage(pendingAdd)}}
	s.chatStore.Save(c)
	if w := sessionRequest(t, s, http.MethodPost, "/chat/"+c.ID+"/apply", "admin", `{}`, "application/json"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("apply = %d %s", w.Code, w.Body)
	}
}

func TestPurgeDeletesEveryChatFromInfo(t *testing.T) {
	s := chatServer(t, textAnswer("x"))
	s.chatStore.Create("octocat")
	s.chatStore.Create("someone-else")
	w := sessionRequest(t, s, http.MethodPost, "/chat/purge", "admin", "", "application/x-www-form-urlencoded")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/info" || s.chatStore.Count() != 0 {
		t.Errorf("purge = %d -> %s, %d left", w.Code, w.Header().Get("Location"), s.chatStore.Count())
	}
	if w := sessionRequest(t, s, http.MethodPost, "/chat/purge", "push", "", "application/x-www-form-urlencoded"); w.Code != http.StatusForbidden {
		t.Errorf("push may not purge: %d", w.Code)
	}
}

func TestTranscriptRowsGroupLogsBetweenTheUserAndTheAnswer(t *testing.T) {
	c := &chat.Chat{Messages: []chat.Message{
		{Role: "user", Content: "reconcile"},
		{Role: "assistant", Reasoning: "look at august first", ToolCalls: []chat.ToolCall{{ID: "c1", Type: "function", Function: chat.FunctionCall{Name: "get_actuals", Arguments: `{"month":"2026-08"}`}}}},
		{Role: "tool", ToolCallID: "c1", Name: "get_actuals", Content: `{"month":"2026-08","transactions":[]}`},
		{Role: "assistant", Content: "Nothing recorded yet. **Attach** the statement."},
		{Role: "user", Content: "System note: the user approved and committed data/actuals/2026-08.json."},
	}}
	rows := rowsOf(c)
	kinds := []string{}
	for _, r := range rows {
		kinds = append(kinds, r.Kind)
	}
	if strings.Join(kinds, " ") != "user logs answer note" {
		t.Fatalf("rows = %v", kinds)
	}
	logs := rows[1].Logs
	if len(logs) != 3 || logs[0].Kind != "thinking" || logs[1].Kind != "call" || logs[1].Name != "get_actuals" || logs[2].Kind != "result" {
		t.Errorf("logs = %+v", logs)
	}
	if !strings.Contains(string(rows[2].HTML), "<strong>Attach</strong>") {
		t.Errorf("answer html = %s", rows[2].HTML)
	}
	if strings.HasPrefix(rows[3].Text, "System note") {
		t.Error("the note prefix is not shown")
	}
}
