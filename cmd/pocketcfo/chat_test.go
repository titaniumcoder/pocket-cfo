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
		w.Header().Set("Content-Type", "text/event-stream")
		if n < len(answers) {
			io.WriteString(w, sseOf(answers[n]))
		} else {
			io.WriteString(w, sseOf(answers[len(answers)-1]))
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

func slowChatRunner(t *testing.T, s *server) *chat.Runner {
	t.Helper()
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sseOf(textAnswer("slow answer")))
	}))
	t.Cleanup(stub.Close)
	client, err := chat.NewClient(chat.ClientConfig{Key: s.cfg.openAIKey, BaseURL: stub.URL, Model: s.cfg.openAIModel, HTTP: &http.Client{Timeout: 5 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	return &chat.Runner{Client: client, Store: s.chatStore, Service: s.apiService()}
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

func TestAnOpenQuestionRendersOptionsAndAnAnswerStartsATurn(t *testing.T) {
	s := chatServer(t, textAnswer("thanks"))
	c, _ := s.chatStore.Create("octocat")
	c.Messages = []chat.Message{{Role: "user", Content: "x"}, {Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "q1", Type: "function", Function: chat.FunctionCall{Name: chat.AskUserTool, Arguments: `{}`}}}}}
	c.Question = &chat.Question{ToolCallID: "q1", Text: "Which account?", Options: []string{"Private Checking", "Company Checking"}, AllowFreeText: true}
	s.chatStore.Save(c)
	page := sessionRequest(t, s, http.MethodGet, "/chat/"+c.ID, "admin", "", "").Body.String()
	if !strings.Contains(page, `data-call="q1"`) || strings.Count(page, `class="button-outline question-option"`) != 2 || !strings.Contains(page, `id="question-free"`) {
		t.Errorf("question card missing:\n%s", page)
	}
	if w := sessionRequest(t, s, http.MethodPost, "/chat/"+c.ID+"/turn", "admin", `{"answer":{"tool_call_id":"q1","text":"Private Checking"}}`, "application/json"); w.Code != http.StatusAccepted {
		t.Fatalf("answer = %d %s", w.Code, w.Body)
	}
	sessionRequest(t, s, http.MethodGet, "/chat/"+c.ID+"/events", "admin", "", "")
	saved, _ := s.chatStore.Load("octocat", c.ID)
	if saved.Question != nil || len(saved.Messages) != 4 {
		t.Errorf("saved = %+v", saved.Messages)
	}
}

func sseOf(completion string) string {
	var full struct {
		Choices []struct {
			FinishReason string          `json:"finish_reason"`
			Message      json.RawMessage `json:"message"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(completion), &full); err != nil || len(full.Choices) == 0 {
		return "data: " + completion + "\n\ndata: [DONE]\n\n"
	}
	var delta map[string]any
	json.Unmarshal(full.Choices[0].Message, &delta)
	if calls, ok := delta["tool_calls"].([]any); ok {
		for i, c := range calls {
			c.(map[string]any)["index"] = i
		}
	}
	first, _ := json.Marshal(map[string]any{"id": "c", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}})
	last := map[string]any{"id": "c", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": full.Choices[0].FinishReason}}}
	if len(full.Usage) > 0 {
		last["usage"] = full.Usage
	}
	second, _ := json.Marshal(last)
	return "data: " + string(first) + "\n\n: keepalive\n\ndata: " + string(second) + "\n\ndata: [DONE]\n\n"
}
