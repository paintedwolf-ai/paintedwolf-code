package summarize

import (
	"encoding/json"
	"fmt"
)

// ActionArguments is directly accepted by the named tool.
type ActionArguments struct {
	Path    string   `json:"path,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	Task    string   `json:"task,omitempty"`
	Pattern string   `json:"pattern,omitempty"`
	Cursor  string   `json:"cursor,omitempty"`
	Offset  int      `json:"offset,omitempty"`
	Limit   int      `json:"limit,omitempty"`
}

type actionWire struct {
	Tool   string          `json:"tool"`
	Args   ActionArguments `json:"args"`
	Kind   string          `json:"kind"`
	Reason string          `json:"reason"`
}

func (a NextAction) MarshalJSON() ([]byte, error) {
	args := ActionArguments{Path: a.Path, Paths: a.Paths, Task: a.Task, Pattern: a.Pattern, Cursor: a.Cursor}
	if a.Tool == "read" && a.Lines != "" {
		var end int
		if _, err := fmt.Sscanf(a.Lines, "%d-%d", &args.Offset, &end); err != nil || args.Offset < 1 || end < args.Offset {
			return nil, fmt.Errorf("invalid source range %q", a.Lines)
		}
		args.Limit = end - args.Offset + 1
	}
	kind := "inspect"
	if a.Cursor != "" {
		kind = "continue"
	}
	return json.Marshal(actionWire{Tool: a.Tool, Args: args, Kind: kind, Reason: a.Why})
}

func (a *NextAction) UnmarshalJSON(raw []byte) error {
	var wire actionWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	*a = NextAction{Tool: wire.Tool, Path: wire.Args.Path, Paths: wire.Args.Paths, Task: wire.Args.Task,
		Pattern: wire.Args.Pattern, Cursor: wire.Args.Cursor, Why: wire.Reason}
	if wire.Args.Offset > 0 && wire.Args.Limit > 0 {
		a.Lines = fmt.Sprintf("%d-%d", wire.Args.Offset, wire.Args.Offset+wire.Args.Limit-1)
	}
	return nil
}
