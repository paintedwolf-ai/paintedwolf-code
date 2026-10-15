package toolcommand

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// CommandFileWrite describes a command or pipeline that writes a file via
// heredoc, here-string, stdin pass-through, or shell wrapper redirection.
type CommandFileWrite struct {
	Command string
	Path    string
	Append  bool
	Calls   []ReplacementCall
	Exact   bool
}

const maxShellWrapperDepth = 3

// InspectCommandFileWrite detects shell file-writing commands and constructs
// exact native write/mkdir ReplacementCalls whenever the payload is literal.
func InspectCommandFileWrite(ctx context.Context, toolName string, args map[string]any, activeDir string) (CommandFileWrite, bool) {
	switch toolName {
	case "command", "verify", "terminal_open":
	default:
		return CommandFileWrite{}, false
	}
	if len(args) == 0 {
		return CommandFileWrite{}, false
	}
	rawCommand, _ := args["command"].(string)
	pipelineStages, hasPipeline := extractPipelineSlice(args["pipeline"])
	if strings.TrimSpace(rawCommand) == "" && len(pipelineStages) == 0 {
		return CommandFileWrite{}, false
	}
	stdinText, hasStdin := args["stdin"].(string)
	hasStdin = hasStdin && stdinText != ""
	stdoutTo, _ := args["stdout_to"].(string)
	stdoutTo = strings.TrimSpace(stdoutTo)
	appendFlag, _ := args["append"].(bool)
	cwd, _ := args["cwd"].(string)
	cwd = strings.TrimSpace(cwd)

	exactEnv := fileWriteEnvelopeExactEligible(toolName, args, cwd)
	displayCmd := commandsurface.PrimaryCommandLine(args, nil)
	if displayCmd == "" {
		displayCmd = strings.TrimSpace(rawCommand)
	}

	var (
		matched   bool
		firstPath string
		firstApp  bool
		calls     []ReplacementCall
		allExact  bool
	)

	if hasPipeline && strings.TrimSpace(rawCommand) == "" {
		joined := strings.Join(pipelineStages, " | ")
		matched, firstPath, firstApp, calls, allExact = analyzeScriptFileWrites(ctx, joined, stdinText, hasStdin, stdoutTo, appendFlag, 0)
		allExact = allExact && exactEnv
	} else {
		matched, firstPath, firstApp, calls, allExact = analyzeScriptFileWrites(ctx, rawCommand, stdinText, hasStdin, stdoutTo, appendFlag, 0)
		allExact = allExact && exactEnv
	}

	if !matched {
		return CommandFileWrite{}, false
	}

	env := Envelope{Command: displayCmd, cwd: cwd}
	if timeout, ok := integerArg(args["timeout_ms"]); ok && timeout > 0 {
		env.timeoutMS = timeout
	}
	if allExact && len(calls) > 0 {
		carried, ok := carryCommandEnvelope(env, calls)
		if !ok {
			allExact = false
			calls = nil
		} else {
			for _, call := range carried {
				if !replacementPathsRepresentable(call) {
					allExact = false
					calls = nil
					break
				}
			}
			if allExact {
				calls = carried
			}
		}
	} else {
		allExact = false
		calls = nil
	}

	if firstPath != "" && cwd != "" && cwd != "." && !filepath.IsAbs(firstPath) && !strings.HasPrefix(firstPath, "@") {
		if len(calls) > 0 {
			for _, call := range calls {
				if call.Tool == "write" {
					if p, ok := call.Args["path"].(string); ok && p != "" {
						firstPath = p
						break
					}
				}
			}
		} else if projectRelativePath(cwd) && projectRelativePath(firstPath) {
			firstPath = filepath.ToSlash(filepath.Join(cwd, firstPath))
		}
	}

	return CommandFileWrite{
		Command: displayCmd,
		Path:    firstPath,
		Append:  firstApp,
		Calls:   calls,
		Exact:   allExact && len(calls) > 0,
	}, true
}

// FileWriteReject builds the structured USE_WRITE_NATIVE rejection.
func FileWriteReject(fw CommandFileWrite, profile string, calls []ReplacementCall, toolLoadable bool) *toolrejection.ToolReject {
	entry, _ := commandEquivalenceEntryFor("write")
	data := map[string]any{
		"command":        strings.TrimSpace(fw.Command),
		"profile_id":     profile,
		"suggested_tool": "write",
		"example":        entry.Example,
		"args_hint":      entry.ArgsHint,
		"tool_loadable":  toolLoadable,
		"reason":         "shell_file_write",
	}
	if fw.Path != "" {
		data["path"] = fw.Path
	}
	if len(calls) > 0 {
		data["replacement_calls"] = calls
	}
	return &toolrejection.ToolReject{
		Code: replacementCode("write"),
		Data: data,
	}
}

func fileWriteEnvelopeExactEligible(toolName string, args map[string]any, cwd string) bool {
	if toolName != "command" && toolName != "verify" {
		return false
	}
	if cwd != "" && !filepath.IsAbs(cwd) && !projectRelativePath(cwd) {
		return false
	}
	for name, val := range args {
		switch name {
		case "command", "pipeline", "cwd", "stdin", "stdout_to", "append":
		case "timeout_ms":
			if timeout, ok := integerArg(val); !ok || timeout <= 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func extractPipelineSlice(raw any) ([]string, bool) {
	switch items := raw.(type) {
	case []string:
		return items, len(items) > 0
	case []any:
		out := make([]string, 0, len(items))
		for _, item := range items {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, len(out) > 0
	default:
		return nil, false
	}
}

func analyzeScriptFileWrites(
	ctx context.Context,
	script, stdinText string,
	hasStdin bool,
	stdoutTo string,
	appendFlag bool,
	depth int,
) (matched bool, firstPath string, firstApp bool, calls []ReplacementCall, allExact bool) {
	if depth > maxShellWrapperDepth {
		return false, "", false, nil, false
	}
	tokens := lexBashScript(script)
	stmts := parseBashStatements(tokens)
	if len(stmts) == 0 {
		return false, "", false, nil, false
	}

	allExact = true
	if len(stmts) > 1 && hasStdin {
		allExact = false
	}

	for _, stmt := range stmts {
		if stmt.op == "||" || stmt.op == "&" {
			allExact = false
		}
		stmtStdout := stdoutTo
		stmtAppend := appendFlag
		if len(stmts) > 1 {
			stmtStdout = ""
			stmtAppend = false
		}
		stMatched, stPath, stApp, stCalls, stExact := analyzeStatementFileWrite(ctx, stmt, stdinText, hasStdin && len(stmts) == 1, stmtStdout, stmtAppend, depth)
		if stMatched {
			matched = true
			if firstPath == "" && stPath != "" {
				firstPath = stPath
				firstApp = stApp
			}
			if allExact && stExact && len(stCalls) > 0 {
				calls = append(calls, stCalls...)
			} else {
				allExact = false
			}
			continue
		}
		if mkdirCall, ok := analyzeStatementExactMkdir(stmt); ok && allExact {
			calls = append(calls, mkdirCall)
			continue
		}
		allExact = false
	}

	if !matched {
		if tsPath, tsApp, ok := treeSitterBashFileWrite(ctx, script, stdoutTo); ok {
			return true, tsPath, tsApp, nil, false
		}
		return false, "", false, nil, false
	}
	if !allExact {
		calls = nil
	}
	return true, firstPath, firstApp, calls, allExact
}

func analyzeStatementExactMkdir(stmt bashStatement) (ReplacementCall, bool) {
	if len(stmt.stages) != 1 {
		return ReplacementCall{}, false
	}
	st := stmt.stages[0]
	if len(st.env) > 0 || len(st.redirects) > 0 || len(st.heredocs) > 0 || len(st.herestrings) > 0 || st.subshell != "" || len(st.argv) < 2 {
		return ReplacementCall{}, false
	}
	if filepath.Base(st.argv[0].value) != "mkdir" || !st.argv[0].literal {
		return ReplacementCall{}, false
	}
	args := make([]string, 0, len(st.argv)-1)
	for _, w := range st.argv[1:] {
		if !w.literal || w.value != strings.TrimSpace(w.value) {
			return ReplacementCall{}, false
		}
		args = append(args, w.value)
	}
	call, ok := exactFilesystemReplacement("mkdir", args)
	if !ok || !replacementPathsRepresentable(call) {
		return ReplacementCall{}, false
	}
	return call, true
}
