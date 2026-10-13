package chat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/api"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

const idRent = "00000000-0000-4000-8000-000000000001"

const testBudget = `{"groups":[{"name":"Housing","kind":"private","categories":[{"id":"` + idRent + `","name":"Rent","amount":900}]}]}`

const testAugust = `{"month":"2026-08","transactions":[{"id":"t1","date":"2026-08-01","description":"OLD","amount":900,"account":"A","category":"` + idRent + `"}]}`

type memStore struct {
	mu    sync.Mutex
	files map[string][]byte
	puts  int
}

func (m *memStore) Get(_ context.Context, p string) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[p]
	if !ok {
		return nil, "", api.ErrNotFound
	}
	return b, "sha-" + p, nil
}

func (m *memStore) Put(_ context.Context, p string, content []byte, _, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts++
	m.files[p] = content
	return "sha2-" + p, nil
}

func (m *memStore) List(_ context.Context, _ string) ([]string, error) { return nil, nil }

type scripted struct {
	mu       sync.Mutex
	answers  []string
	status   int
	requests []map[string]any
}

func (s *scripted) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request is not JSON: %v", err)
		}
		s.mu.Lock()
		s.requests = append(s.requests, body)
		n := len(s.requests)
		s.mu.Unlock()
		if s.status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s.status)
			io.WriteString(w, `{"error":{"message":"down"}}`)
			return
		}
		if n > len(s.answers) {
			n = len(s.answers)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sseOf(s.answers[n-1]))
	})
}

func callAnswer(id, name, args string) string {
	b, _ := json.Marshal(args)
	return `{"id":"c","object":"chat.completion","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"` + id + `","type":"function","function":{"name":"` + name + `","arguments":` + string(b) + `}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`
}

func textAnswer(text string) string {
	return `{"id":"c","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"` + text + `"}}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`
}

func runner(t *testing.T, s *scripted) (*Runner, *memStore) {
	t.Helper()
	srv := httptest.NewServer(s.handler(t))
	t.Cleanup(srv.Close)
	client, err := NewClient(ClientConfig{Key: "k", BaseURL: srv.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{Dir: filepath.Join(t.TempDir(), "chats")}
	if err := store.Open(); err != nil {
		t.Fatal(err)
	}
	mem := &memStore{files: map[string][]byte{"data/actuals/2026-08.json": []byte(testAugust)}}
	svc := &api.Service{
		Now:     func() time.Time { return time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC) },
		Budget:  &tracker.Budget{FS: fstest.MapFS{"budget.json": &fstest.MapFile{Data: []byte(testBudget)}}},
		Actuals: &tracker.Actuals{FS: fstest.MapFS{"actuals/2026-08.json": &fstest.MapFile{Data: []byte(testAugust)}}},
	}
	return &Runner{Client: client, Store: store, Service: svc, MaxRounds: 4, Now: svc.Now}, mem
}

func collect() (func(Event) error, *[]Event) {
	var events []Event
	return func(e Event) error { events = append(events, e); return nil }, &events
}

func TestATurnRunsReadToolsAndSavesEveryRound(t *testing.T) {
	s := &scripted{answers: []string{
		callAnswer("c1", "get_actuals", `{"month":"2026-08"}`),
		textAnswer("August is recorded."),
	}}
	r, _ := runner(t, s)
	c, _ := r.Store.Create("octocat")
	emit, events := collect()

	err := r.Run(context.Background(), c, Input{Text: "how is august?"}, emit)
	if err != nil {
		t.Fatal(err)
	}

	roles := []string{}
	for _, m := range c.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, " ") != "user assistant tool assistant" {
		t.Fatalf("messages = %v", roles)
	}
	if !strings.Contains(c.Messages[2].Content, `"OLD"`) || c.Messages[2].ToolCallID != "c1" {
		t.Errorf("the read tool's result must reach the model: %+v", c.Messages[2])
	}
	if c.Usage.PromptTokens != 15 || c.Usage.CompletionTokens != 3 {
		t.Errorf("usage = %+v", c.Usage)
	}

	kinds := []string{}
	for _, e := range *events {
		kinds = append(kinds, e.Event)
	}
	if strings.Join(kinds, " ") != "assistant tool assistant done" {
		t.Errorf("events = %v", kinds)
	}

	saved, err := r.Store.Load("octocat", c.ID)
	if err != nil || len(saved.Messages) != 4 {
		t.Errorf("the chat on disk lags: %d messages, %v", len(saved.Messages), err)
	}

	first := s.requests[0]
	if first["messages"].([]any)[0].(map[string]any)["role"] != "system" {
		t.Error("the system prompt must lead")
	}
	names := map[string]string{}
	for _, tool := range first["tools"].([]any) {
		fn := tool.(map[string]any)["function"].(map[string]any)
		names[fn["name"].(string)] = fn["description"].(string)
	}
	if _, ok := names["search_transactions"]; !ok {
		t.Error("the search tool must be offered")
	}
	if len(s.requests[1]["messages"].([]any)) != 4 {
		t.Errorf("the second request must carry the tool result: %d messages", len(s.requests[1]["messages"].([]any)))
	}
}

func TestAModelErrorIsAnEventAndTheUserMessageStays(t *testing.T) {
	s := &scripted{status: http.StatusInternalServerError}
	r, _ := runner(t, s)
	c, _ := r.Store.Create("octocat")
	emit, events := collect()
	if err := r.Run(context.Background(), c, Input{Text: "hi"}, emit); err != nil {
		t.Fatal(err)
	}
	last := (*events)[len(*events)-1]
	if last.Event != "error" || !strings.Contains(last.Error, "500") {
		t.Errorf("events = %+v", *events)
	}
	saved, _ := r.Store.Load("octocat", c.ID)
	if len(saved.Messages) != 1 || saved.Messages[0].Role != "user" {
		t.Errorf("saved = %+v", saved.Messages)
	}
}

func TestTheLoopStopsAfterTheRoundCap(t *testing.T) {
	s := &scripted{answers: []string{callAnswer("c1", "get_actuals", `{"month":"2026-08"}`)}}
	r, _ := runner(t, s)
	c, _ := r.Store.Create("octocat")
	emit, events := collect()
	if err := r.Run(context.Background(), c, Input{Text: "loop"}, emit); err != nil {
		t.Fatal(err)
	}
	last := (*events)[len(*events)-1]
	if last.Event != "error" || !strings.Contains(last.Error, "4 tool rounds") {
		t.Errorf("last event = %+v", last)
	}
	if len(c.Messages) != 1+2*4 {
		t.Errorf("%d messages", len(c.Messages))
	}
}

func TestAnEmptyTurnIsRefused(t *testing.T) {
	r, _ := runner(t, &scripted{answers: []string{textAnswer("x")}})
	c, _ := r.Store.Create("octocat")
	if err := r.Run(context.Background(), c, Input{Text: "  "}, func(Event) error { return nil }); err != ErrEmptyTurn {
		t.Errorf("got %v", err)
	}
}

func TestAQuestionStopsTheTurnAndAnAnswerResumesIt(t *testing.T) {
	s := &scripted{answers: []string{
		callAnswer("q1", AskUserTool, `{"question":"Which account is this?","options":["Private Checking","Company Checking"]}`),
		textAnswer("Thanks, Private Checking it is."),
	}}
	r, _ := runner(t, s)
	c, _ := r.Store.Create("octocat")
	emit, events := collect()
	if err := r.Run(context.Background(), c, Input{Text: "reconcile"}, emit); err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, e := range *events {
		kinds = append(kinds, e.Event)
	}
	if strings.Join(kinds, " ") != "assistant question done" {
		t.Fatalf("events = %v", kinds)
	}
	if c.Question == nil || c.Question.ToolCallID != "q1" || len(c.Question.Options) != 2 || !c.Question.AllowFreeText {
		t.Fatalf("question = %+v", c.Question)
	}
	saved, _ := r.Store.Load("octocat", c.ID)
	if saved.Question == nil || len(saved.Messages) != 2 {
		t.Fatalf("saved = %+v", saved)
	}

	emit, events = collect()
	if err := r.Run(context.Background(), saved, Input{Answer: &Answer{ToolCallID: "q1", Text: "Private Checking"}}, emit); err != nil {
		t.Fatal(err)
	}
	if saved.Question != nil {
		t.Error("the question must be closed by the answer")
	}
	roles := []string{}
	for _, m := range saved.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, " ") != "user assistant tool assistant" || saved.Messages[2].ToolCallID != "q1" || !strings.Contains(saved.Messages[2].Content, "Private Checking") {
		t.Errorf("messages = %v / %+v", roles, saved.Messages[2])
	}
	sent := s.requests[1]["messages"].([]any)
	last := sent[len(sent)-1].(map[string]any)
	if last["role"] != "tool" || last["tool_call_id"] != "q1" {
		t.Errorf("the answer must reach the model as the tool result: %v", last)
	}
	if (*events)[len(*events)-1].Event != "done" || !strings.Contains(saved.Messages[3].Content, "Thanks") {
		t.Errorf("the turn must continue to the answer: %+v", *events)
	}
}

func TestAPlainMessageAnswersAnOpenQuestionToo(t *testing.T) {
	s := &scripted{answers: []string{textAnswer("ok")}}
	r, _ := runner(t, s)
	c, _ := r.Store.Create("octocat")
	c.Messages = []Message{{Role: "user", Content: "x"}, {Role: "assistant", ToolCalls: []ToolCall{{ID: "q1", Type: "function", Function: FunctionCall{Name: AskUserTool, Arguments: `{"question":"Which?"}`}}}}}
	c.Question = &Question{ToolCallID: "q1", Text: "Which?", AllowFreeText: true}
	if err := r.Run(context.Background(), c, Input{Text: "neither, skip it"}, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if c.Question != nil || c.Messages[2].Role != "tool" || !strings.Contains(c.Messages[2].Content, "skip it") || c.Messages[3].Role != "user" {
		t.Errorf("messages = %+v", c.Messages)
	}
}

func TestTheAskUserToolIsOfferedFirst(t *testing.T) {
	defs, err := Definitions(nil)
	if err != nil || len(defs) != 1 || defs[0].Name != AskUserTool {
		t.Fatalf("defs = %+v %v", defs, err)
	}
	if _, ok := defs[0].Parameters["properties"].(map[string]any)["options"]; !ok {
		t.Error("the question tool takes options")
	}
}
