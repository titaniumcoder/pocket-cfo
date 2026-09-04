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

const completionTimeout = 180 * time.Second

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
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
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
	resp, err := c.api.Chat.Completions.New(ctx, params, perRequest...)
	if err != nil {
		return Completion{}, describe(err)
	}
	return decodeCompletion(resp)
}

func (c *Client) params(messages []Message, tools []ToolDef) (openai.ChatCompletionNewParams, error) {
	params := openai.ChatCompletionNewParams{
		Model: c.model,
		Store: openai.Bool(false),
	}
	for i, m := range messages {
		raw, err := json.Marshal(m)
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

func decodeCompletion(resp *openai.ChatCompletion) (Completion, error) {
	if len(resp.Choices) == 0 {
		return Completion{}, bodyError(resp.RawJSON())
	}
	choice := resp.Choices[0]
	var msg Message
	if err := json.Unmarshal([]byte(choice.Message.RawJSON()), &msg); err != nil {
		return Completion{}, fmt.Errorf("chat: the model's answer is not a message: %v", err)
	}
	if msg.Role == "" {
		msg.Role = "assistant"
	}
	return Completion{
		Message:      msg,
		FinishReason: string(choice.FinishReason),
		Usage:        Usage{PromptTokens: resp.Usage.PromptTokens, CompletionTokens: resp.Usage.CompletionTokens},
	}, nil
}

func bodyError(raw string) error {
	var body struct {
		Error struct {
			Code    any    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(raw), &body) == nil && body.Error.Message != "" {
		return fmt.Errorf("the model endpoint refused the request: %s", body.Error.Message)
	}
	return errors.New("the model endpoint answered without a choice")
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
