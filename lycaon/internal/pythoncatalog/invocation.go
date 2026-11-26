package pythoncatalog

import (
	"fmt"
	"strings"
)

// ExecutionMode identifies how a Python invocation enters __main__.
type ExecutionMode string

const (
	ModeCode    ExecutionMode = "code"
	ModeScript  ExecutionMode = "script"
	ModeModule  ExecutionMode = "module"
	ModeStdin   ExecutionMode = "stdin"
	ModeVersion ExecutionMode = "version"
	ModeHelp    ExecutionMode = "help"
)

// DirectToolRequest is the validated argument payload for the direct `python` tool.
type DirectToolRequest struct {
	Mode   ExecutionMode
	Code   string
	Script string
	Module string
	Cwd    string
	Args   []string
}

var allowedDirectToolKeys = map[string]bool{
	"code":   true,
	"script": true,
	"module": true,
	"cwd":    true,
	"args":   true,
}

// ValidateDirectToolArgs validates arguments for the direct `python` tool schema.
// Exactly one of code, script, or module must be provided as a non-empty string.
// cwd and args are optional; args entries are preserved verbatim without shell interpolation.
func ValidateDirectToolArgs(raw map[string]any) (DirectToolRequest, error) {
	if raw == nil {
		return DirectToolRequest{}, fmt.Errorf("python tool requires exactly one of code, script, or module")
	}
	for k := range raw {
		if !allowedDirectToolKeys[k] {
			return DirectToolRequest{}, fmt.Errorf("python tool received unknown argument %q", k)
		}
	}
	var req DirectToolRequest
	modes := 0
	if v, ok := raw["code"]; ok && v != nil {
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return DirectToolRequest{}, fmt.Errorf("python tool argument code must be a non-empty string")
		}
		req.Code = s
		req.Mode = ModeCode
		modes++
	}
	if v, ok := raw["script"]; ok && v != nil {
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return DirectToolRequest{}, fmt.Errorf("python tool argument script must be a non-empty string")
		}
		req.Script = s
		req.Mode = ModeScript
		modes++
	}
	if v, ok := raw["module"]; ok && v != nil {
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return DirectToolRequest{}, fmt.Errorf("python tool argument module must be a non-empty string")
		}
		req.Module = s
		req.Mode = ModeModule
		modes++
	}
	if modes != 1 {
		return DirectToolRequest{}, fmt.Errorf("python tool requires exactly one of code, script, or module (got %d)", modes)
	}
	if v, ok := raw["cwd"]; ok && v != nil {
		s, ok := v.(string)
		if !ok {
			return DirectToolRequest{}, fmt.Errorf("python tool argument cwd must be a string")
		}
		req.Cwd = s
	}
	if v, ok := raw["args"]; ok && v != nil {
		args, err := parseStringSlice(v)
		if err != nil {
			return DirectToolRequest{}, err
		}
		req.Args = args
	}
	return req, nil
}

func parseStringSlice(v any) ([]string, error) {
	switch typed := v.(type) {
	case []string:
		return append([]string(nil), typed...), nil
	case []any:
		out := make([]string, len(typed))
		for i, item := range typed {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("python tool argument args[%d] must be a string", i)
			}
			out[i] = s
		}
		return out, nil
	default:
		return nil, fmt.Errorf("python tool argument args must be an array of strings")
	}
}

// ParsedCommandLine represents a parsed native `python` / `python3` command-line invocation.
// Option parsing ends at -c, -m, -, or a script path, and all remaining tokens are preserved
// byte-for-byte in Argv.
type ParsedCommandLine struct {
	Executable string
	Mode       ExecutionMode
	Flags      []string
	Target     string
	Argv       []string
}

var supportedInterpreterBoolFlags = map[string]bool{
	"-B": true,
	"-E": true,
	"-I": true,
	"-O": true,
	"-P": true,
	"-S": true,
	"-q": true,
	"-s": true,
	"-u": true,
	"-v": true,
}

var supportedInterpreterValueFlags = map[string]bool{
	"-W": true,
	"-X": true,
}

// ParsePythonCommandLine parses a native `python` or `python3` argv slice.
// Option parsing stops at the operand boundary (-c <code>, -m <module>, -, or <script>)
// and forwards all remaining arguments verbatim into ParsedCommandLine.Argv.
func ParsePythonCommandLine(argv []string) (ParsedCommandLine, error) {
	if len(argv) == 0 {
		return ParsedCommandLine{}, fmt.Errorf("empty python command line")
	}
	exe := argv[0]
	if exe != "python" && exe != "python3" {
		return ParsedCommandLine{}, fmt.Errorf("unsupported interpreter command %q (expected python or python3)", exe)
	}
	out := ParsedCommandLine{Executable: exe}
	for i := 1; i < len(argv); i++ {
		tok := argv[i]
		if tok == "-" {
			out.Mode = ModeStdin
			out.Target = "-"
			out.Argv = append([]string(nil), argv[i+1:]...)
			return out, nil
		}
		if tok == "-c" {
			if i+1 >= len(argv) {
				return ParsedCommandLine{}, fmt.Errorf("%s: option -c requires an argument", exe)
			}
			out.Mode = ModeCode
			out.Target = argv[i+1]
			out.Argv = append([]string(nil), argv[i+2:]...)
			return out, nil
		}
		if strings.HasPrefix(tok, "-c") && len(tok) > 2 {
			out.Mode = ModeCode
			out.Target = tok[2:]
			out.Argv = append([]string(nil), argv[i+1:]...)
			return out, nil
		}
		if tok == "-m" {
			if i+1 >= len(argv) {
				return ParsedCommandLine{}, fmt.Errorf("%s: option -m requires an argument", exe)
			}
			out.Mode = ModeModule
			out.Target = argv[i+1]
			out.Argv = append([]string(nil), argv[i+2:]...)
			return out, nil
		}
		if strings.HasPrefix(tok, "-m") && len(tok) > 2 {
			out.Mode = ModeModule
			out.Target = tok[2:]
			out.Argv = append([]string(nil), argv[i+1:]...)
			return out, nil
		}
		if tok == "-V" || tok == "--version" {
			out.Mode = ModeVersion
			return out, nil
		}
		if tok == "-h" || tok == "--help" {
			out.Mode = ModeHelp
			return out, nil
		}
		if supportedInterpreterBoolFlags[tok] {
			out.Flags = append(out.Flags, tok)
			continue
		}
		if supportedInterpreterValueFlags[tok] {
			if i+1 >= len(argv) {
				return ParsedCommandLine{}, fmt.Errorf("%s: option %s requires an argument", exe, tok)
			}
			out.Flags = append(out.Flags, tok, argv[i+1])
			i++
			continue
		}
		if strings.HasPrefix(tok, "-") {
			return ParsedCommandLine{}, fmt.Errorf("%s: unsupported interpreter option %q", exe, tok)
		}
		out.Mode = ModeScript
		out.Target = tok
		out.Argv = append([]string(nil), argv[i+1:]...)
		return out, nil
	}
	// No script or -c/-m operand: read program from stdin.
	out.Mode = ModeStdin
	out.Target = "-"
	return out, nil
}

// RoutingCodePythonToolRequired is the recoverable policy rejection code emitted when
// an agent offered the direct `python` tool attempts a bare `python`/`python3` launch in `command`.
const RoutingCodePythonToolRequired = "PYTHON_TOOL_REQUIRED"

// RoutingDecision describes whether a command-language interpreter launch must be routed
// to the direct `python` tool before interpreter startup.
type RoutingDecision struct {
	Blocked bool
	Code    string
	Command string
	Reason  string
}

// CheckBareInterpreterRouting detects bare `python` or `python3` launches in `command`
// when the direct `python` tool is offered on the active surface.
// Explicit `external -- python3` selections and path-qualified project interpreters
// (e.g. `.venv/bin/python`, `/usr/bin/python3`) are allowed.
func CheckBareInterpreterRouting(argv []string, directPythonOffered bool) RoutingDecision {
	if !directPythonOffered || len(argv) == 0 {
		return RoutingDecision{}
	}
	cmd := strings.TrimSpace(argv[0])
	if cmd == "external" {
		return RoutingDecision{}
	}
	if strings.ContainsAny(cmd, `/\`) {
		return RoutingDecision{}
	}
	if cmd == "python" || cmd == "python3" {
		return RoutingDecision{
			Blocked: true,
			Code:    RoutingCodePythonToolRequired,
			Command: cmd,
			Reason:  "Bare interpreter launch stopped before startup; use the direct python tool or explicit external -- python3.",
		}
	}
	return RoutingDecision{}
}
