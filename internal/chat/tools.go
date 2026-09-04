package chat

import (
	"encoding/json"

	"github.com/titaniumcoder/pocket-cfo/internal/api"
)

const stagedNote = "\n\nSTAGED: this call is recorded as a pending change and nothing is committed until the user approves it in the Changes panel. The result you get back is a dry run against the current data plus the other pending changes. Do not call it again for the same lines while they are pending; tell the user what is waiting for approval instead."

func Definitions(tools []api.Tool) ([]ToolDef, error) {
	out := make([]ToolDef, 0, len(tools))
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
