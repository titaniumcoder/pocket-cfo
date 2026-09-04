package main

import (
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
	s := apiServer(t, apiTestToken, "prod")
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

func TestATurnStreamsEventsAndTheChatPageShowsThem(t *testing.T) {
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
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("turn = %d %s %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"event":"assistant"`) || !strings.Contains(lines[1], `"event":"done"`) {
		t.Fatalf("stream = %q", lines)
	}

	w = sessionRequest(t, s, http.MethodGet, location, "admin", "", "")
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "Hello from the model") || !strings.Contains(body, "Attached file `a.csv`") {
		t.Errorf("chat page = %d:\n%s", w.Code, body)
	}
	if !strings.Contains(body, `<title>Pocket CFO — hi</title>`) {
		t.Error("the first line names the chat")
	}
	w = sessionRequest(t, s, http.MethodGet, "/chat", "admin", "", "")
	if !strings.Contains(w.Body.String(), `href="`+location+`"`) {
		t.Error("the list must link the chat")
	}
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
