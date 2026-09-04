package chat

import (
	"encoding/json"

	"github.com/titaniumcoder/pocket-cfo/internal/api"
)

const stagedNote = "\n\nSTAGED: this call is recorded as a pending change and nothing is committed until the user approves it in the Changes panel. The result you get back is a dry run against the current data plus the other pending changes. Do not call it again for the same lines while they are pending; tell the user what is waiting for approval instead."

const AskUserTool = "ask_user"

var askUser = ToolDef{
	Name:        AskUserTool,
	Description: "Ask the user a question and wait for the answer. This is the normal way to resolve anything you are not sure about — which account a statement belongs to, which category a line goes to, whether a line is ignored or untracked, what to do about a category that does not exist — and it is always better than assuming. Give concrete options the user can click, one question at a time, and keep the question short. The turn ends when you call this; the answer arrives as this call's result in the next turn, so say nothing else after asking.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question":        map[string]any{"type": "string", "description": "the question, one or two sentences, with the context the user needs to answer it"},
			"options":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "the answers the user can click, in the order you would offer them; two to six, each a short label. Leave it out only when the answer is free text, such as a name or an amount"},
			"allow_free_text": map[string]any{"type": "boolean", "description": "whether the user may answer in their own words instead of an option. Default true"},
		},
		"required":             []string{"question"},
		"additionalProperties": false,
	},
}

type askUserArgs struct {
	Question      string   `json:"question"`
	Options       []string `json:"options"`
	AllowFreeText *bool    `json:"allow_free_text"`
}

func Definitions(tools []api.Tool) ([]ToolDef, error) {
	out := make([]ToolDef, 0, len(tools)+1)
	out = append(out, askUser)
	for _, t := range tools {
		raw, err := json.Marshal(t.InputSchema)
		if err != nil {
			return nil, err
		}
		var params map[string]any
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		description := t.Description
		if !t.ReadOnly {
			description += stagedNote
		}
		out = append(out, ToolDef{Name: t.Name, Description: description, Parameters: params})
	}
	return out, nil
}
