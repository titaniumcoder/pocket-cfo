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

const testAugust = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],"transactions":[{"id":"t1","date":"2026-08-01","description":"OLD","amount":900,"account":"A","category":"` + idRent + `"}]}`

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
		Actuals: &tracker.Actuals{FS: fstest.MapFS{}},
		Store:   api.NewStaging(mem),
	}
	return &Runner{Client: client, Store: store, Service: svc, MaxRounds: 4, Now: svc.Now}, mem
}

func collect() (func(Event) error, *[]Event) {
	var events []Event
	return func(e Event) error { events = append(events, e); return nil }, &events
}

func TestATurnRunsReadToolsStagesWritesAndSavesEveryRound(t *testing.T) {
	s := &scripted{answers: []string{
		callAnswer("c1", "get_actuals", `{"month":"2026-08"}`),
		callAnswer("c2", "add_transactions", `{"transactions":[{"id":"n1","date":"2026-08-03","description":"NEW","amount":12,"account":"A","category":"`+idRent+`"}]}`),
		textAnswer("Staged one line for August."),
	}}
	r, mem := runner(t, s)
	c, _ := r.Store.Create("octocat")
	emit, events := collect()

	err := r.Run(context.Background(), c, Input{Text: "reconcile august", Files: []Upload{{Name: "august.csv", Content: "date;text;amount\n03.08.2026;NEW;-12,00\n"}}}, emit)
	if err != nil {
		t.Fatal(err)
	}

	roles := []string{}
	for _, m := range c.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, " ") != "user assistant tool assistant tool assistant" {
		t.Fatalf("messages = %v", roles)
	}
	if !strings.Contains(c.Messages[0].Content, "Attached file `august.csv`") || !strings.Contains(c.Messages[0].Content, "03.08.2026;NEW") {
		t.Errorf("the file must be inside the user message: %q", c.Messages[0].Content)
	}
	if c.Title != "reconcile august" || len(c.Files) != 1 || c.Files[0].Name != "august.csv" {
		t.Errorf("title %q files %+v", c.Title, c.Files)
	}
	if !strings.Contains(c.Messages[2].Content, `"OLD"`) || c.Messages[2].ToolCallID != "c1" {
		t.Errorf("the read tool's result must reach the model: %+v", c.Messages[2])
	}
	if !strings.Contains(c.Messages[4].Content, `"staged":true`) || !strings.Contains(c.Messages[4].Content, `"added":1`) {
		t.Errorf("the write must answer with the dry run: %s", c.Messages[4].Content)
	}
	if len(c.Pending) != 1 || c.Pending[0].Tool != "add_transactions" || !strings.Contains(c.Pending[0].Summary, `"added":1`) {
		t.Errorf("pending = %+v", c.Pending)
	}
	if mem.puts != 0 {
		t.Errorf("a turn must never commit: %d puts", mem.puts)
	}
	if c.Usage.PromptTokens != 25 || c.Usage.CompletionTokens != 5 {
		t.Errorf("usage = %+v", c.Usage)
	}

	kinds := []string{}
	for _, e := range *events {
		kinds = append(kinds, e.Event)
	}
	if strings.Join(kinds, " ") != "assistant tool assistant tool pending assistant done" {
		t.Errorf("events = %v", kinds)
	}

	saved, err := r.Store.Load("octocat", c.ID)
	if err != nil || len(saved.Messages) != 6 || len(saved.Pending) != 1 {
		t.Errorf("the chat on disk lags: %d messages, %d pending, %v", len(saved.Messages), len(saved.Pending), err)
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
	if _, ok := names["derive_transaction_ids"]; !ok {
		t.Error("the id tool must be offered")
	}
	if !strings.Contains(names["add_transactions"], "STAGED") || strings.Contains(names["get_actuals"], "STAGED") {
		t.Error("only write tools carry the staged note")
	}
	if len(s.requests[1]["messages"].([]any)) != 4 {
		t.Errorf("the second request must carry the tool result: %d messages", len(s.requests[1]["messages"].([]any)))
	}
}

func TestPendingChangesAreReplayedBeforeTheModelSpeaks(t *testing.T) {
	s := &scripted{answers: []string{textAnswer("ok")}}
	r, _ := runner(t, s)
	c, _ := r.Store.Create("octocat")
	c.Pending = []PendingChange{
		{Tool: "add_transactions", Arguments: json.RawMessage(`{"transactions":[{"id":"n1","date":"2026-08-03","description":"NEW","amount":12,"account":"A","category":"` + idRent + `"}]}`)},
		{Tool: "add_transactions", Arguments: json.RawMessage(`{"transactions":[{"id":"bad","date":"2026-08-03","description":"X","amount":1,"account":"A","category":"nope"}]}`)},
	}
	emit, events := collect()
	if err := r.Run(context.Background(), c, Input{Text: "and?"}, emit); err != nil {
		t.Fatal(err)
	}
	if (*events)[0].Event != "pending" || (*events)[1].Event != "pending" {
		t.Fatalf("events = %+v", *events)
	}
	if c.Pending[0].Error != "" || !strings.Contains(c.Pending[0].Summary, `"added":1`) {
		t.Errorf("first pending = %+v", c.Pending[0])
	}
	if c.Pending[1].Error == "" {
		t.Errorf("the bad pending change must carry its error: %+v", c.Pending[1])
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
