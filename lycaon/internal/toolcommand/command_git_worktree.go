package toolcommand

import (
	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gitargv"
	"path/filepath"
	"slices"
	"strings"
)

func gitReplacement(args []string, projectDir string) (ReplacementCall, bool) {
	if len(args) == 0 {
		return ReplacementCall{}, false
	}
	switch args[0] {
	case "commit":
		return gitAmendReplacement(args[1:])
	case "status":
		return gitStatusReplacement(args[1:])
	case "diff":
		return gitDiffReplacement(args[1:], projectDir)
	case "log":
		return gitHistoryReplacement(args[1:])
	case "checkout", "switch":
		return gitCheckoutReplacement(args[0], args[1:], projectDir)
	case "merge":
		return gitMergeReplacement(args[1:])
	case "stash":
		return gitStashReplacement(args[1:])
	case "show":
		return gitShowReplacement(args[1:])
	case "blame":
		if len(args) == 2 && !strings.HasPrefix(args[1], "-") {
			return ReplacementCall{Tool: "git_blame", Args: map[string]any{"path": args[1]}}, true
		}
	case "rev-parse":
		if len(args) > 1 {
			for _, ref := range args[1:] {
				if strings.HasPrefix(ref, "-") {
					return ReplacementCall{}, false
				}
			}
			return ReplacementCall{Tool: "git_ref", Args: map[string]any{"refs": stringsToAny(args[1:])}}, true
		}
	case "branch":
		if len(args) == 1 {
			return ReplacementCall{Tool: "git_branches", Args: map[string]any{}}, true
		}
	case "restore":
		return gitRestoreReplacement(args[1:])
	}
	return ReplacementCall{}, false
}

func gitRestoreReplacement(args []string) (ReplacementCall, bool) {
	mapped := map[string]any{}
	var paths []string
	optionsDone := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !optionsDone && arg == "--" {
			optionsDone = true
			continue
		}
		if !optionsDone {
			switch {
			case arg == "--staged" || arg == "-S":
				mapped["staged"] = true
				continue
			case arg == "--worktree" || arg == "-W":
				mapped["worktree"] = true
				continue
			case arg == "--source" || arg == "-s":
				i++
				if i >= len(args) || strings.TrimSpace(args[i]) == "" {
					return ReplacementCall{}, false
				}
				mapped["source"] = args[i]
				continue
			case strings.HasPrefix(arg, "--source="):
				source := strings.TrimPrefix(arg, "--source=")
				if source == "" {
					return ReplacementCall{}, false
				}
				mapped["source"] = source
				continue
			case strings.HasPrefix(arg, "-"):
				return ReplacementCall{}, false
			}
		}
		// The native tool reads literal pathspecs; a glob would change meaning.
		if !gitLiteralOperand(arg) {
			return ReplacementCall{}, false
		}
		paths = append(paths, arg)
	}
	if len(paths) == 0 {
		return ReplacementCall{}, false
	}
	mapped["paths"] = stringsToAny(paths)
	return ReplacementCall{Tool: "git_restore", Args: mapped}, true
}

func gitStatusReplacement(args []string) (ReplacementCall, bool) {
	var paths []string
	pathsOnly := false
	ignored := false
	for _, arg := range args {
		switch {
		case pathsOnly:
			paths = append(paths, arg)
		case arg == "--":
			pathsOnly = true
		case arg == "--ignored" || arg == "--ignored=matching" || arg == "--ignored=traditional":
			ignored = true
		case strings.HasPrefix(arg, "--"):
			if !gitStatusLongFlagEquivalent(arg) {
				return ReplacementCall{}, false
			}
		case strings.HasPrefix(arg, "-") && arg != "-":
			if !gitStatusShortFlagsEquivalent(arg[1:]) {
				return ReplacementCall{}, false
			}
		default:
			paths = append(paths, arg)
		}
	}
	if ignored && len(paths) == 0 {
		return ReplacementCall{}, false
	}
	mapped := map[string]any{}
	if len(paths) > 0 {
		mapped["paths"] = stringsToAny(paths)
	}
	if ignored {
		mapped["ignored"] = true
	}
	call := ReplacementCall{Tool: "git_status", Args: mapped}
	return call, replacementPathsRepresentable(call)
}

func gitDiffReplacement(args []string, projectDir string) (ReplacementCall, bool) {
	mapped := map[string]any{}
	var paths []string
	baseRef := ""
	headRef := ""
	pathsOnly := false
	for _, arg := range args {
		switch {
		case pathsOnly:
			paths = append(paths, arg)
		case arg == "--":
			pathsOnly = true
		case arg == "--stat" || arg == "--numstat":
			mapped["stat"] = true
		case arg == "--staged" || arg == "--cached":
			mapped["staged"] = true
		case strings.HasPrefix(arg, "-"):
			return ReplacementCall{}, false
		case confine.ProjectPathExists(arg, projectDir):
			paths = append(paths, arg)
		case baseRef == "" && gitargv.ValidateRefArg(arg) == nil && !strings.Contains(arg, ".."):
			baseRef = arg
		case headRef == "" && gitargv.ValidateRefArg(arg) == nil && !strings.Contains(arg, ".."):
			headRef = arg
		default:
			return ReplacementCall{}, false
		}
	}
	if baseRef != "" {
		mapped["base_ref"] = baseRef
	}
	if headRef != "" {
		if mapped["staged"] == true {
			return ReplacementCall{}, false
		}
		mapped["head_ref"] = headRef
	}
	if len(paths) > 0 {
		mapped["paths"] = stringsToAny(paths)
	}
	call := ReplacementCall{Tool: "git_diff", Args: mapped}
	return call, replacementPathsRepresentable(call)
}

func gitStatusLongFlagEquivalent(flag string) bool {
	switch flag {
	case "--short", "--long", "--porcelain", "--porcelain=v1", "--porcelain=v2",
		"--branch", "--ahead-behind", "--no-ahead-behind",
		"--untracked-files", "--untracked-files=all", "--untracked-files=normal":
		return true
	}
	return false
}

func gitStatusShortFlagsEquivalent(cluster string) bool {
	for i := 0; i < len(cluster); i++ {
		switch cluster[i] {
		case 's', 'b', 'z':
		case 'u':
			mode := cluster[i+1:]
			return mode == "" || mode == "all" || mode == "normal"
		default:
			return false
		}
	}
	return true
}

func gitCommitSequence(elements []argv.SequenceElement) (ReplacementCall, bool) {
	if len(elements) != 2 || elements[1].Connector != argv.ConnectorAnd ||
		commandGrammarFor[filepath.Base(elements[0].Name)] != "git" ||
		commandGrammarFor[filepath.Base(elements[1].Name)] != "git" {
		return ReplacementCall{}, false
	}
	if len(elements[0].Args) < 2 || elements[0].Args[0] != "add" {
		return ReplacementCall{}, false
	}
	paths, ok := operandsOnly(elements[0].Args[1:])
	if !ok || len(paths) == 0 {
		return ReplacementCall{}, false
	}
	commit := elements[1].Args
	if len(commit) > 0 && commit[0] == "commit" {
		if call, ok := gitAmendReplacement(commit[1:]); ok {
			selected := call.Args["paths"].([]any)
			for _, path := range paths {
				if !slices.Contains(selected, any(path)) {
					return ReplacementCall{}, false
				}
			}
			return call, replacementPathsRepresentable(call)
		}
	}
	if len(commit) != 3 || commit[0] != "commit" || (commit[1] != "-m" && commit[1] != "--message") {
		return ReplacementCall{}, false
	}
	call := ReplacementCall{Tool: "git_commit", Args: map[string]any{"message": commit[2], "paths": stringsToAny(paths)}}
	return call, replacementPathsRepresentable(call)
}
