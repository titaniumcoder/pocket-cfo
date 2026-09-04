package chat

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const toolCallAnswer = `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_actuals","arguments":"{\"month\":\"2026-08\"}"}}]}}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`

func stub(t *testing.T, status int, answer string) (*Client, *[]map[string]any, *http.Header) {
	t.Helper()
	var bodies []map[string]any
	var header http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request is not JSON: %v", err)
		}
		bodies = append(bodies, body)
		header = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(ClientConfig{Key: "sk-test", BaseURL: srv.URL, Model: "acme/model-1", ExtraBody: `{"provider":{"zdr":true}}`, Referer: "https://cfo.example"})
	if err != nil {
		t.Fatal(err)
	}
	return c, &bodies, &header
}

func TestCompleteRoundTripsMessagesToolsAndExtraBody(t *testing.T) {
	c, bodies, header := stub(t, http.StatusOK, toolCallAnswer)
	messages := []Message{
		{Role: "system", Content: "be brief"},
		{Role: "user", Content: "reconcile august"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_0", Type: "function", Function: FunctionCall{Name: "list_accounts", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "call_0", Content: `{"accounts":[]}`},
	}
	tools := []ToolDef{{Name: "get_actuals", Description: "the month", Parameters: map[string]any{"type": "object", "properties": map[string]any{"month": map[string]any{"type": "string"}}}}}

	out, err := c.Complete(context.Background(), messages, tools)
	if err != nil {
		t.Fatal(err)
	}
	if out.FinishReason != "tool_calls" || len(out.Message.ToolCalls) != 1 || out.Message.ToolCalls[0].Function.Name != "get_actuals" || out.Message.ToolCalls[0].Function.Arguments != `{"month":"2026-08"}` {
		t.Errorf("tool call not decoded: %+v", out)
	}
	if out.Message.Role != "assistant" || out.Usage.PromptTokens != 12 || out.Usage.CompletionTokens != 3 {
		t.Errorf("completion = %+v", out)
	}

	if len(*bodies) != 1 {
		t.Fatalf("%d requests, want 1", len(*bodies))
	}
	body := (*bodies)[0]
	if body["model"] != "acme/model-1" || body["store"] != false {
		t.Errorf("model/store: %v %v", body["model"], body["store"])
	}
	if provider, _ := body["provider"].(map[string]any); provider["zdr"] != true {
		t.Errorf("extra body not merged: %v", body["provider"])
	}
	sent := body["messages"].([]any)
	if len(sent) != 4 {
		t.Fatalf("messages sent: %v", sent)
	}
	assistant := sent[2].(map[string]any)
	calls := assistant["tool_calls"].([]any)
	if calls[0].(map[string]any)["id"] != "call_0" {
		t.Errorf("assistant tool call lost: %v", assistant)
	}
	toolMsg := sent[3].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_0" {
		t.Errorf("tool message lost: %v", toolMsg)
	}
	fn := body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "get_actuals" || fn["description"] != "the month" || fn["parameters"].(map[string]any)["type"] != "object" {
		t.Errorf("tool definition: %v", fn)
	}
	if header.Get("Authorization") != "Bearer sk-test" || header.Get("X-Title") != "Pocket CFO" || header.Get("HTTP-Referer") != "https://cfo.example" {
		t.Errorf("headers: %v", *header)
	}
}

func TestReasoningIsKeptForThePageButNeverSentBack(t *testing.T) {
	c, bodies, _ := stub(t, http.StatusOK, `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hi","reasoning":"first I checked the month"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	out, err := c.Complete(context.Background(), []Message{{Role: "assistant", Content: "earlier", Reasoning: "old thoughts"}, {Role: "user", Content: "x"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Message.Reasoning != "first I checked the month" {
		t.Errorf("reasoning not captured: %+v", out.Message)
	}
	sent := (*bodies)[0]["messages"].([]any)[0].(map[string]any)
	if _, leaked := sent["reasoning"]; leaked {
		t.Error("reasoning must not be sent back to the provider")
	}
}

func TestAnErrorInsideA200BodyIsAnError(t *testing.T) {
	c, _, _ := stub(t, http.StatusOK, `{"error":{"code":403,"message":"no ZDR endpoint for this model"},"user_id":"u"}`)
	_, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "no ZDR endpoint") {
		t.Fatalf("want the endpoint's message, got %v", err)
	}
}

func TestAnHTTPErrorCarriesItsStatus(t *testing.T) {
	c, bodies, _ := stub(t, http.StatusUnauthorized, `{"error":{"message":"bad key","type":"auth"}}`)
	_, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("want 401 with the message, got %v", err)
	}
	if len(*bodies) != 1 {
		t.Errorf("a 401 must not be retried, got %d requests", len(*bodies))
	}
}

func TestClientNeedsAKeyAndAModelAndAnObjectExtraBody(t *testing.T) {
	if _, err := NewClient(ClientConfig{Key: "k"}); err == nil {
		t.Error("a missing model must be refused")
	}
	if _, err := NewClient(ClientConfig{Key: "k", Model: "m", ExtraBody: `[1]`}); err == nil {
		t.Error("an extra body that is not an object must be refused")
	}
	if _, err := NewClient(ClientConfig{Key: "k", Model: "m"}); err != nil {
		t.Errorf("a key and a model suffice: %v", err)
	}
}

func TestOpenAISDKIsImportedOnce(t *testing.T) {
	const sdk = "github.com/openai/openai-go"
	var importers []string
	err := filepath.Walk("../..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if path != "../.." && (strings.HasPrefix(info.Name(), ".") || info.Name() == "build") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			return nil
		}
		for _, imp := range file.Imports {
			if strings.Contains(imp.Path.Value, sdk) {
				importers = append(importers, filepath.ToSlash(path))
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(importers) != 1 || !strings.HasSuffix(importers[0], "internal/chat/openai.go") {
		t.Errorf("the SDK is imported by %v; it belongs only in internal/chat/openai.go, so deleting that file drops the dependency", importers)
	}
}
