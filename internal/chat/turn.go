package chat

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/api"
)

//go:embed system.md
var systemPrompt string

const (
	DefaultMaxRounds = 25
	maxFileBytes     = 1 << 20
	maxSummaryBytes  = 4000
	titleLength      = 60
)

type Input struct {
	Text  string   `json:"text"`
	Files []Upload `json:"files,omitempty"`
}

type Upload struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type Event struct {
	Event   string         `json:"event"`
	Message *Message       `json:"message,omitempty"`
	Pending *PendingChange `json:"pending,omitempty"`
	Index   int            `json:"index,omitempty"`
	Error   string         `json:"error,omitempty"`
	Usage   *Usage         `json:"usage,omitempty"`
}

type Runner struct {
	Client    *Client
	Store     *Store
	Service   *api.Service
	MaxRounds int
	Now       func() time.Time
}

var ErrEmptyTurn = errors.New("say something or attach a file")

func (r *Runner) Run(ctx context.Context, c *Chat, in Input, emit func(Event) error) error {
	if err := r.appendUserMessage(c, in); err != nil {
		return err
	}
	if err := r.replayPending(ctx, c, emit); err != nil {
		return err
	}
	tools, err := Definitions(r.Service.Tools())
	if err != nil {
		return err
	}
	byName := map[string]api.Tool{}
	for _, t := range r.Service.Tools() {
		byName[t.Name] = t
	}

	rounds := r.MaxRounds
	if rounds <= 0 {
		rounds = DefaultMaxRounds
	}
	for round := 0; round < rounds; round++ {
		out, err := r.Client.Complete(ctx, append(r.system(), c.Messages...), tools)
		if err != nil {
			return r.fail(c, emit, err.Error())
		}
		c.Usage.PromptTokens += out.Usage.PromptTokens
		c.Usage.CompletionTokens += out.Usage.CompletionTokens
		assistant := out.Message
		c.Messages = append(c.Messages, assistant)
		if len(assistant.ToolCalls) == 0 {
			break
		}
		var events []Event
		for _, tc := range assistant.ToolCalls {
			result, ev := r.execute(ctx, c, byName, tc)
			c.Messages = append(c.Messages, result)
			events = append(events, Event{Event: "tool", Message: &result})
			if ev != nil {
				events = append(events, *ev)
			}
		}
		if err := r.Store.Save(c); err != nil {
			return err
		}
		if err := emit(Event{Event: "assistant", Message: &assistant}); err != nil {
			return err
		}
		for _, ev := range events {
			if err := emit(ev); err != nil {
				return err
			}
		}
		if round == rounds-1 {
			return r.fail(c, emit, fmt.Sprintf("stopped after %d tool rounds without an answer — say how to continue", rounds))
		}
	}
	if err := r.Store.Save(c); err != nil {
		return err
	}
	last := c.Messages[len(c.Messages)-1]
	if last.Role == "assistant" && len(last.ToolCalls) == 0 {
		if err := emit(Event{Event: "assistant", Message: &last}); err != nil {
			return err
		}
	}
	return emit(Event{Event: "done", Usage: &c.Usage})
}

func (r *Runner) appendUserMessage(c *Chat, in Input) error {
	text := strings.TrimSpace(in.Text)
	if text == "" && len(in.Files) == 0 {
		return ErrEmptyTurn
	}
	var b strings.Builder
	b.WriteString(text)
	for _, f := range in.Files {
		if len(f.Content) > maxFileBytes {
			return fmt.Errorf("%s is larger than %d bytes", f.Name, maxFileBytes)
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "Attached file `%s`:\n```\n%s\n```", f.Name, strings.TrimRight(f.Content, "\n"))
		c.Files = append(c.Files, File{Name: f.Name, Bytes: len(f.Content), Message: len(c.Messages)})
	}
	c.Messages = append(c.Messages, Message{Role: "user", Content: b.String()})
	if c.Title == "" || c.Title == "New chat" {
		c.Title = titleOf(text, in.Files)
	}
	return r.Store.Save(c)
}

func titleOf(text string, files []Upload) string {
	if text == "" && len(files) > 0 {
		text = files[0].Name
	}
	line := strings.SplitN(strings.TrimSpace(text), "\n", 2)[0]
	if runes := []rune(line); len(runes) > titleLength {
		return string(runes[:titleLength-1]) + "…"
	}
	return line
}

func (r *Runner) replayPending(ctx context.Context, c *Chat, emit func(Event) error) error {
	if len(c.Pending) == 0 {
		return nil
	}
	calls := make([]api.ToolCall, 0, len(c.Pending))
	for _, p := range c.Pending {
		calls = append(calls, api.ToolCall{Name: p.Tool, Arguments: p.Arguments})
	}
	for i, s := range api.Replay(ctx, r.Service, calls) {
		c.Pending[i].Summary, c.Pending[i].Error = summarize(s.Result), errorMessage(s.Err)
		if err := emit(Event{Event: "pending", Index: i, Pending: &c.Pending[i]}); err != nil {
			return err
		}
	}
	return r.Store.Save(c)
}

func (r *Runner) execute(ctx context.Context, c *Chat, byName map[string]api.Tool, tc ToolCall) (Message, *Event) {
	reply := func(content string) Message {
		return Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: content}
	}
	tool, ok := byName[tc.Function.Name]
	if !ok {
		return reply(errorJSON(&api.Error{Code: api.CodeInvalidRequest, Message: "no tool named " + tc.Function.Name})), nil
	}
	args := json.RawMessage(strings.TrimSpace(tc.Function.Arguments))
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	if !json.Valid(args) {
		return reply(errorJSON(&api.Error{Code: api.CodeInvalidRequest, Message: "arguments are not JSON"})), nil
	}
	if tool.ReadOnly {
		out, err := tool.Call(ctx, args)
		if err != nil {
			return reply(errorJSON(err)), nil
		}
		return reply(resultJSON(out)), nil
	}
	staged := api.Replay(ctx, r.Service, []api.ToolCall{{Name: tool.Name, Arguments: args}})[0]
	if staged.Err != nil {
		return reply(errorJSON(staged.Err)), nil
	}
	c.Pending = append(c.Pending, PendingChange{Tool: tool.Name, Arguments: args, Summary: summarize(staged.Result)})
	index := len(c.Pending) - 1
	return reply(stagedJSON(staged.Result)), &Event{Event: "pending", Index: index, Pending: &c.Pending[index]}
}

func (r *Runner) system() []Message {
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	return []Message{{Role: "system", Content: systemPrompt + "\nToday is " + now.Format("2006-01-02") + "."}}
}

func (r *Runner) fail(c *Chat, emit func(Event) error, message string) error {
	if err := r.Store.Save(c); err != nil {
		return err
	}
	return emit(Event{Event: "error", Error: message})
}

func resultJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return errorJSON(err)
	}
	return string(b)
}

func stagedJSON(v any) string {
	b, err := json.Marshal(map[string]any{"staged": true, "pending_approval": "nothing is committed until the user approves this change", "dry_run": v})
	if err != nil {
		return errorJSON(err)
	}
	return string(b)
}

func errorJSON(err error) string {
	var e *api.Error
	if !errors.As(err, &e) {
		e = &api.Error{Code: api.CodeInternal, Message: err.Error()}
	}
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": e.Code, "message": e.Message, "details": e.Details}})
	return string(b)
}

func errorMessage(e *api.Error) string {
	if e == nil {
		return ""
	}
	return e.Message
}

func summarize(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	if len(b) > maxSummaryBytes {
		return string(b[:maxSummaryBytes]) + "…"
	}
	return string(b)
}
