package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Service) MCPHandler(version string) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "pocket-cfo", Version: version}, nil)
	for _, t := range s.Tools() {
		server.AddTool(&mcp.Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: t.ReadOnly},
		}, callThrough(t))
	}
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
}

func callThrough(t Tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		out, err := t.Call(ctx, req.Params.Arguments)
		if err != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: errorText(err)}},
			}, nil
		}
		body, err := json.Marshal(out)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{
			StructuredContent: json.RawMessage(body),
			Content:           []mcp.Content{&mcp.TextContent{Text: string(body)}},
		}, nil
	}
}

func errorText(err error) string {
	if e, ok := err.(*Error); ok {
		b, mErr := json.Marshal(map[string]any{"code": e.Code, "message": e.Message, "details": e.Details})
		if mErr == nil {
			return string(b)
		}
	}
	return err.Error()
}
