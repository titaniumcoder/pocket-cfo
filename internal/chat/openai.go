package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

const DefaultBaseURL = "https://api.openai.com/v1"

const (
	completionTimeout = 9 * time.Minute
	completionIdle    = 3 * time.Minute
)

type ClientConfig struct {
	Key       string
	BaseURL   string
	Model     string
	ExtraBody string
	Referer   string
	HTTP      *http.Client
}

type Client struct {
	model string
	extra map[string]any
	api   openai.Client
}

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Reasoning  string     `json:"reasoning,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

func (m Message) outbound() Message {
	m.Reasoning = ""
	return m
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any
}

type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

type Completion struct {
	Message      Message
	FinishReason string
	Usage        Usage
}

func ParseExtraBody(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var extra map[string]any
	if err := json.Unmarshal([]byte(raw), &extra); err != nil {
		return nil, fmt.Errorf("must be a JSON object: %v", err)
	}
	return extra, nil
}

func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.Key == "" || cfg.Model == "" {
		return nil, errors.New("chat: a key and a model are required")
	}
	extra, err := ParseExtraBody(cfg.ExtraBody)
	if err != nil {
		return nil, fmt.Errorf("chat: extra body %v", err)
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	opts := []option.RequestOption{
		option.WithBaseURL(base),
		option.WithAPIKey(cfg.Key),
		option.WithRequestTimeout(completionTimeout),
		option.WithMaxRetries(1),
		option.WithHeader("X-Title", "Pocket CFO"),
	}
	if cfg.Referer != "" {
		opts = append(opts, option.WithHeader("HTTP-Referer", cfg.Referer))
	}
	if cfg.HTTP != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTP))
	}
	return &Client{model: cfg.Model, extra: extra, api: openai.NewClient(opts...)}, nil
}

func (c *Client) Model() string { return c.model }

func (c *Client) Complete(ctx context.Context, messages []Message, tools []ToolDef) (Completion, error) {
	params, err := c.params(messages, tools)
	if err != nil {
		return Completion{}, err
	}
	var perRequest []option.RequestOption
	for key, value := range c.extra {
		perRequest = append(perRequest, option.WithJSONSet(key, value))
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	quiet := false
	idle := time.AfterFunc(completionIdle, func() { quiet = true; cancel() })
	defer idle.Stop()

	stream := c.api.Chat.Completions.NewStreaming(ctx, params, perRequest...)
	var acc openai.ChatCompletionAccumulator
	var reasoning strings.Builder
	var streamed *streamedExtras
	for stream.Next() {
		idle.Reset(completionIdle)
		chunk := stream.Current()
		acc.AddChunk(chunk)
		streamed = readExtras(chunk.RawJSON(), &reasoning, streamed)
	}
	if err := stream.Err(); err != nil {
		if quiet {
			return Completion{}, fmt.Errorf("the model endpoint sent nothing for %s", completionIdle)
		}
		return Completion{}, describe(err)
	}
	if streamed != nil && streamed.err != "" {
		return Completion{}, fmt.Errorf("the model endpoint refused the request: %s", streamed.err)
	}
	return completionOf(acc, reasoning.String())
}

type streamedExtras struct {
	err string
}

func readExtras(raw string, reasoning *strings.Builder, so *streamedExtras) *streamedExtras {
	var chunk struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Choices []struct {
			Delta struct {
				Reasoning        string `json:"reasoning"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(raw), &chunk) != nil {
		return so
	}
	if chunk.Error.Message != "" {
		if so == nil {
			so = &streamedExtras{}
		}
		so.err = chunk.Error.Message
	}
	for _, ch := range chunk.Choices {
		reasoning.WriteString(ch.Delta.Reasoning)
		reasoning.WriteString(ch.Delta.ReasoningContent)
	}
	return so
}

func completionOf(acc openai.ChatCompletionAccumulator, reasoning string) (Completion, error) {
	if len(acc.Choices) == 0 {
		return Completion{}, errors.New("the model endpoint answered without a choice")
	}
	choice := acc.Choices[0]
	msg := Message{Role: "assistant", Content: choice.Message.Content, Reasoning: reasoning}
	for _, tc := range choice.Message.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID: tc.ID, Type: "function",
			Function: FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
		})
	}
	return Completion{
		Message:      msg,
		FinishReason: string(choice.FinishReason),
		Usage:        Usage{PromptTokens: acc.Usage.PromptTokens, CompletionTokens: acc.Usage.CompletionTokens},
	}, nil
}

func (c *Client) params(messages []Message, tools []ToolDef) (openai.ChatCompletionNewParams, error) {
	params := openai.ChatCompletionNewParams{
		Model:         c.model,
		Store:         openai.Bool(false),
		StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)},
	}
	for i, m := range messages {
		raw, err := json.Marshal(m.outbound())
		if err != nil {
			return params, err
		}
		var u openai.ChatCompletionMessageParamUnion
		if err := u.UnmarshalJSON(raw); err != nil {
			return params, fmt.Errorf("chat: message %d (%s): %v", i, m.Role, err)
		}
		params.Messages = append(params.Messages, u)
	}
	for _, t := range tools {
		params.Tools = append(params.Tools, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        t.Name,
			Description: openai.String(t.Description),
			Parameters:  shared.FunctionParameters(t.Parameters),
		}))
	}
	return params, nil
}

func describe(err error) error {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		if apiErr.Message != "" {
			return fmt.Errorf("the model endpoint answered %d: %s", apiErr.StatusCode, apiErr.Message)
		}
		return fmt.Errorf("the model endpoint answered %d %s", apiErr.StatusCode, http.StatusText(apiErr.StatusCode))
	}
	return fmt.Errorf("reaching the model endpoint: %v", err)
}
