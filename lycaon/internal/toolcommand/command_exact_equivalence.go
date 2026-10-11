package toolcommand

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
)

// ReplacementCall is an exact native representation of one command element.
type ReplacementCall struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

func ExactCommandReplacement(ctx context.Context, command, projectDir, sessionScratch string) ([]ReplacementCall, bool) {
	elements, err := argv.SplitSequence(strings.TrimSpace(command))
	if err != nil || len(elements) == 0 {
		return nil, false
	}
	for _, el := range elements {
		// Native tools route no output to files, so a redirection has no lossless translation.
		if len(el.Redirects) > 0 || globsMatch(ctx, el, projectDir, sessionScratch) {
			return nil, false
		}
	}
	if call, ok := gitCommitSequence(elements); ok {
		return []ReplacementCall{call}, true
	}
	if len(elements) != 1 {
		return httpSequenceReplacement(elements)
	}
	el := elements[0]
	program := filepath.Base(strings.TrimSpace(el.Name))
	args, ok := exactProgramReplacement(program, el.Args, command, projectDir)
	if !ok || !replacementPathsRepresentable(args) {
		return nil, false
	}
	return []ReplacementCall{args}, true
}

// globsMatch reports an unquoted pattern the command boundary would expand.
// An unmatched pattern stays literal, so its translation is still exact.
func globsMatch(ctx context.Context, el argv.SequenceElement, projectDir, sessionScratch string) bool {
	if len(el.Globs) == 0 {
		return false
	}
	_, expanded, err := commandsurface.ExpandGlobs(ctx,
		[]exec.Stage{{Name: el.Name, Args: el.Args, Globs: el.Globs}},
		commandsurface.GlobScope{Dir: projectDir, Readable: func(abs string) bool {
			return !confine.ControlPlanePathDenied(abs, false, sessionScratch)
		}})
	return expanded || err != nil
}

func exactProgramReplacement(program string, args []string, raw, projectDir string) (ReplacementCall, bool) {
	if habit, ok := confine.ReadHabitFor(raw, projectDir); ok {
		mapped := map[string]any{"path": habit.Path}
		if habit.Offset > 0 {
			mapped["offset"] = habit.Offset
		}
		if habit.Limit > 0 {
			mapped["limit"] = habit.Limit
		}
		return ReplacementCall{Tool: "read", Args: mapped}, true
	}
	grammar, declared := commandGrammarFor[program]
	if !declared {
		return ReplacementCall{}, false
	}
	switch grammar {
	case "sleep":
		return sleepReplacement(args)
	case "curl":
		return curlReplacement(args)
	case "list_dir":
		path := "."
		seenPath := false
		hidden := false
		optionsDone := false
		for _, arg := range args {
			if !optionsDone && arg == "--" {
				optionsDone = true
				continue
			}
			if !optionsDone && strings.HasPrefix(arg, "-") {
				if arg == "-" || (strings.HasPrefix(arg, "--") && arg != "--all") ||
					(!strings.HasPrefix(arg, "--") && strings.TrimLeft(strings.TrimPrefix(arg, "-"), "alh1") != "") {
					return ReplacementCall{}, false
				}
				hidden = hidden || arg == "--all" || strings.Contains(strings.TrimPrefix(arg, "-"), "a")
				continue
			}
			if seenPath {
				return ReplacementCall{}, false
			}
			path = arg
			seenPath = true
		}
		return ReplacementCall{Tool: "list_dir", Args: map[string]any{
			"path": path, "include_hidden": hidden, "max_depth": 1,
		}}, true
	case "stat":
		paths, ok := operandsOnly(args)
		return ReplacementCall{Tool: "stat", Args: map[string]any{"paths": stringsToAny(paths)}}, ok && len(paths) > 0
	case "wc":
		words := true
		var paths []string
		optionsDone := false
		for _, arg := range args {
			if !optionsDone && arg == "--" {
				optionsDone = true
				continue
			}
			if optionsDone {
				paths = append(paths, arg)
				continue
			}
			switch arg {
			case "-l":
				words = false
			case "-w":
				words = true
			default:
				if strings.HasPrefix(arg, "-") {
					return ReplacementCall{}, false
				}
				paths = append(paths, arg)
			}
		}
		return ReplacementCall{Tool: "wc", Args: map[string]any{"paths": stringsToAny(paths), "words": words}}, len(paths) > 0
	case "delete", "copy", "move", "mkdir", "chmod", "chown", "diff", "extract_zip":
		return exactFilesystemReplacement(grammar, args)
	case "jq_json", "jq_yaml":
		if len(args) != 2 || strings.HasPrefix(args[0], "-") {
			return ReplacementCall{}, false
		}
		format := "json"
		if grammar == "jq_yaml" {
			format = "yaml"
		}
		return ReplacementCall{Tool: "jq", Args: map[string]any{"query": args[0], "path": args[1], "format": format}}, true
	case "grep":
		return grepReplacement(program, args, projectDir)
	case "find", "tree":
		return findReplacement(grammar, args)
	case "git":
		return gitReplacement(args, projectDir)
	}
	return ReplacementCall{}, false
}

func exactFilesystemReplacement(grammar string, args []string) (ReplacementCall, bool) {
	switch grammar {
	case "delete":
		paths, ok := operandsOnly(args)
		return ReplacementCall{Tool: "delete", Args: map[string]any{"paths": stringsToAny(paths), "files_only": true}}, ok && len(paths) > 0
	case "copy", "move":
		values, ok := operandsOnly(args)
		if !ok || len(values) != 2 {
			return ReplacementCall{}, false
		}
		key, tool := "copies", "copy"
		if grammar == "move" {
			key, tool = "moves", "move"
		}
		return ReplacementCall{Tool: tool, Args: map[string]any{key: []any{map[string]any{"from": values[0], "to": values[1]}}}}, true
	case "mkdir":
		var paths []string
		parents := false
		optionsDone := false
		for _, arg := range args {
			if !optionsDone && arg == "--" {
				optionsDone = true
				continue
			}
			if !optionsDone && (arg == "-p" || arg == "--parents") {
				parents = true
				continue
			}
			if !optionsDone && strings.HasPrefix(arg, "-") {
				return ReplacementCall{}, false
			}
			paths = append(paths, arg)
		}
		return ReplacementCall{Tool: "mkdir", Args: map[string]any{"paths": stringsToAny(paths)}}, parents && len(paths) > 0
	case "chmod":
		if len(args) < 2 || strings.HasPrefix(args[0], "-") {
			return ReplacementCall{}, false
		}
		paths, ok := operandsOnly(args[1:])
		return ReplacementCall{Tool: "chmod", Args: map[string]any{"mode": args[0], "paths": stringsToAny(paths)}}, ok && len(paths) > 0
	case "chown":
		if len(args) < 2 || strings.HasPrefix(args[0], "-") {
			return ReplacementCall{}, false
		}
		owner, group, hasGroup := strings.Cut(args[0], ":")
		if !hasGroup || owner == "" || group == "" || !isCurrentOwnershipSpec(owner, group) {
			return ReplacementCall{}, false
		}
		paths, ok := operandsOnly(args[1:])
		if !ok || len(paths) == 0 {
			return ReplacementCall{}, false
		}
		mapped := map[string]any{"owner": owner, "group": group, "paths": stringsToAny(paths)}
		return ReplacementCall{Tool: "chown", Args: mapped}, true
	case "diff":
		values, ok := operandsOnly(args)
		if !ok || len(values) != 2 {
			return ReplacementCall{}, false
		}
		return ReplacementCall{Tool: "diff", Args: map[string]any{"path_a": values[0], "path_b": values[1]}}, true
	case "extract_zip":
		if len(args) == 3 && args[1] == "-d" {
			return ReplacementCall{Tool: "extract_archive", Args: map[string]any{"path": args[0], "dest": args[2]}}, true
		}
	}
	return ReplacementCall{}, false
}

func sleepReplacement(args []string) (ReplacementCall, bool) {
	if len(args) != 1 {
		return ReplacementCall{}, false
	}
	seconds, err := strconv.ParseFloat(args[0], 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 1 || seconds > 1800 {
		return ReplacementCall{}, false
	}
	return ReplacementCall{Tool: "wait", Args: map[string]any{
		"timeout_ms": int(seconds * 1000), "reason": "waiting for a timer",
	}}, true
}

func operandsOnly(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	optionsDone := false
	for _, arg := range args {
		if !optionsDone && arg == "--" {
			optionsDone = true
			continue
		}
		if !optionsDone && strings.HasPrefix(arg, "-") {
			return nil, false
		}
		out = append(out, arg)
	}
	return out, true
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for i := range values {
		out[i] = values[i]
	}
	return out
}

func grepReplacement(program string, args []string, projectDir string) (ReplacementCall, bool) {
	mapped := map[string]any{"structural": false, "include_hidden": program == "grep"}
	var values []string
	recursive := program == "rg"
	optionsDone := false
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if !optionsDone && arg == "--" {
			optionsDone = true
			continue
		}
		if optionsDone {
			values = append(values, arg)
			continue
		}
		switch arg {
		case "-r":
			if program != "grep" {
				return ReplacementCall{}, false
			}
			recursive = true
		case "-n", "--line-number":
		case "-i", "--ignore-case":
			mapped["case_insensitive"] = true
		case "--hidden":
			if program != "rg" {
				return ReplacementCall{}, false
			}
			mapped["include_hidden"] = true
		case "-g", "--glob":
			if program != "rg" {
				return ReplacementCall{}, false
			}
			if len(args) == 0 {
				return ReplacementCall{}, false
			}
			if _, exists := mapped["path_glob"]; exists {
				return ReplacementCall{}, false
			}
			mapped["path_glob"] = args[0]
			args = args[1:]
		default:
			if strings.HasPrefix(arg, "-") {
				return ReplacementCall{}, false
			}
			values = append(values, arg)
		}
	}
	if len(values) == 0 || len(values) > 2 || values[0] == "" || values[0] != strings.TrimSpace(values[0]) {
		return ReplacementCall{}, false
	}
	// Basic regex operators do not share the native pattern grammar.
	if program == "grep" && strings.ContainsAny(values[0], `\?+{}|()`+"\n\r") {
		return ReplacementCall{}, false
	}
	if !recursive {
		if len(values) != 2 || strings.ContainsAny(values[0], `\.*[]^$?+{}|()`) {
			return ReplacementCall{}, false
		}
		file := values[1]
		if !filepath.IsAbs(file) {
			file = joinCommandCwd(projectDir, file)
		}
		info, err := os.Lstat(file)
		if err != nil || !info.Mode().IsRegular() {
			return ReplacementCall{}, false
		}
	}
	mapped["pattern"] = values[0]
	if len(values) == 2 {
		mapped["path"] = values[1]
	}
	return ReplacementCall{Tool: "grep", Args: mapped}, true
}

func findReplacement(grammar string, args []string) (ReplacementCall, bool) {
	mapped := map[string]any{"path": "."}
	seenPath := false
	if grammar == "tree" {
		for i := 0; i < len(args); i++ {
			if args[i] == "-L" {
				i++
				if i >= len(args) {
					return ReplacementCall{}, false
				}
				n, err := strconv.Atoi(args[i])
				if err != nil || n < 1 || n > 8 {
					return ReplacementCall{}, false
				}
				mapped["max_depth"] = n
				continue
			}
			if strings.HasPrefix(args[i], "-") || seenPath {
				return ReplacementCall{}, false
			}
			mapped["path"] = args[i]
			seenPath = true
		}
		return ReplacementCall{Tool: "find", Args: mapped}, true
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-name":
			i++
			if i >= len(args) {
				return ReplacementCall{}, false
			}
			mapped["name_glob"] = args[i]
		case "-type":
			i++
			if i >= len(args) || (args[i] != "f" && args[i] != "d") {
				return ReplacementCall{}, false
			}
			if args[i] == "f" {
				mapped["type"] = "file"
			} else {
				mapped["type"] = "dir"
			}
		case "-maxdepth":
			i++
			if i >= len(args) {
				return ReplacementCall{}, false
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 || n > 8 {
				return ReplacementCall{}, false
			}
			mapped["max_depth"] = n
		default:
			if strings.HasPrefix(args[i], "-") || seenPath {
				return ReplacementCall{}, false
			}
			mapped["path"] = args[i]
			seenPath = true
		}
	}
	return ReplacementCall{Tool: "find", Args: mapped}, true
}

// gitStatusReplacement accepts formatting flags that preserve the native result's facts.

// gitDiffReplacement resolves existing paths before refs and supports one comparison ref.

// gitStatusShortFlagsEquivalent accepts a cluster such as -s, -sb, -z, or -suall,
// where -u consumes the rest of the cluster as its untracked mode.

func replacementCode(tool string) string {
	if entry, ok := commandEquivalenceEntryFor(tool); ok {
		return entry.RedirectCode
	}
	return "USE_NATIVE_TOOL"
}

func ReplacementReject(command, profile string, calls []ReplacementCall) *toolrejection.ToolReject {
	if len(calls) == 0 {
		return nil
	}
	entry, _ := commandEquivalenceEntryFor(calls[0].Tool)
	return &toolrejection.ToolReject{Code: replacementCode(calls[0].Tool), Data: map[string]any{
		"command": strings.TrimSpace(command), "profile_id": profile, "suggested_tool": calls[0].Tool,
		"replacement_calls": calls, "example": entry.Example, "args_hint": entry.ArgsHint,
		"reason": "exact_native_equivalence",
	}}
}
