package summarize

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ActionArguments is directly accepted by the named tool.
type ActionArguments struct {
	Command string   `json:"command,omitempty"`
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
	args := ActionArguments{Path: a.Path, Paths: a.Paths, Task: a.Task, Pattern: a.Pattern, Cursor: a.Cursor, Command: a.Command}
	if a.Tool == "read" && a.Lines != "" {
		var last int
		if _, err := fmt.Sscanf(a.Lines, "%d-%d", &args.Offset, &last); err != nil || args.Offset < 1 || last < args.Offset || a.Lines != fmt.Sprintf("%d-%d", args.Offset, last) {
			return nil, fmt.Errorf("invalid source range %q", a.Lines)
		}
		args.Limit = last - args.Offset + 1
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
		Pattern: wire.Args.Pattern, Cursor: wire.Args.Cursor, Why: wire.Reason, Command: wire.Args.Command}

	if wire.Args.Offset > 0 && wire.Args.Limit > 0 {
		a.Lines = fmt.Sprintf("%d-%d", wire.Args.Offset, wire.Args.Offset+wire.Args.Limit-1)
	}
	return nil
}

// PortableInspection returns an explicit command action for a caller whose live
// offered entry accepts the portable language. Ordinary structured summarize
// consumers retain their currently reachable actions until that entry activates.
func (a NextAction) PortableInspection() (NextAction, error) {
	switch a.Tool {
	case "read", "grep", "find", "list_dir":
		path, err := quoteActionOperand(a.Path)
		if err != nil {
			return NextAction{}, err
		}
		var command string
		switch a.Tool {
		case "read":
			if a.Lines == "" {
				command = "cat -- " + path
			} else {
				var first, last int
				if _, err := fmt.Sscanf(a.Lines, "%d-%d", &first, &last); err != nil || first < 1 || last < first || a.Lines != fmt.Sprintf("%d-%d", first, last) {
					return NextAction{}, fmt.Errorf("invalid source range %q", a.Lines)
				}
				command = fmt.Sprintf("sed -n '%d,%dp' -- %s", first, last, path)
			}
		case "grep":
			pattern, err := quoteActionOperand(a.Pattern)
			if err != nil {
				return NextAction{}, err
			}
			command = "rg -n -e " + pattern + " -- " + path
		case "find":
			command = "find -- " + path
		case "list_dir":
			command = "ls -a -- " + path
		}
		a.Tool = "command"
		a.Command = command
		a.Path = ""
		a.Paths = nil
		a.Task = ""
		a.Pattern = ""
		a.Cursor = ""
		a.Lines = ""
	}
	return a, nil
}

// A source unit is UTF-8 without NUL. Single-quoted operands preserve literal
// bytes, including dollar signs, substitutions, backslashes and embedded LF.
func quoteActionOperand(value string) (string, error) {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("command action operand cannot be represented in a UTF-8 source unit")
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'", nil
}
