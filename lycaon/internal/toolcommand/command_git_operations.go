package toolcommand

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
)

func gitHistoryReplacement(args []string) (ReplacementCall, bool) {
	mapped := map[string]any{}
	pathsOnly := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--" && !pathsOnly:
			pathsOnly = true
		case pathsOnly:
			if mapped["path"] != nil || !gitLiteralOperand(arg) {
				return ReplacementCall{}, false
			}
			mapped["path"] = arg
		case arg == "--oneline":
		case arg == "--all":
			mapped["all"] = true
		case arg == "-n" || arg == "--max-count":
			i++
			if i >= len(args) || !gitHistoryLimit(mapped, args[i]) {
				return ReplacementCall{}, false
			}
		case strings.HasPrefix(arg, "--max-count="):
			if !gitHistoryLimit(mapped, strings.TrimPrefix(arg, "--max-count=")) {
				return ReplacementCall{}, false
			}
		case strings.HasPrefix(arg, "-n"):
			if !gitHistoryLimit(mapped, strings.TrimPrefix(arg, "-n")) {
				return ReplacementCall{}, false
			}
		case len(arg) > 1 && arg[0] == '-' && arg[1] >= '0' && arg[1] <= '9':
			if !gitHistoryLimit(mapped, arg[1:]) {
				return ReplacementCall{}, false
			}
		case strings.HasPrefix(arg, "-") || mapped["ref"] != nil:
			return ReplacementCall{}, false
		default:
			mapped["ref"] = arg
		}
	}
	return ReplacementCall{Tool: "git_log", Args: mapped}, true
}

func gitShowReplacement(args []string) (ReplacementCall, bool) {
	mapped := map[string]any{}
	pathsOnly := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--" && !pathsOnly:
			pathsOnly = true
		case pathsOnly:
			if mapped["path"] != nil || !gitLiteralOperand(arg) {
				return ReplacementCall{}, false
			}
			mapped["path"] = arg
		case arg == "--stat":
			mapped["stat"] = true
		case arg == "--oneline":
		case strings.HasPrefix(arg, "-"):
			return ReplacementCall{}, false
		case mapped["ref"] == nil:
			ref, path, hasPath := strings.Cut(arg, ":")
			if hasPath {
				if ref == "" || path == "" {
					return ReplacementCall{}, false
				}
				mapped["ref"], mapped["path"] = ref, path
			} else {
				mapped["ref"] = ref
			}
		default:
			return ReplacementCall{}, false
		}
	}
	return ReplacementCall{Tool: "git_show", Args: mapped}, true
}

func gitHistoryLimit(mapped map[string]any, value string) bool {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 80 {
		return false
	}
	mapped["limit"] = n
	return true
}

func gitCheckoutReplacement(command string, args []string, projectDir string) (ReplacementCall, bool) {
	if command == "checkout" && slices.Contains(args, "--") {
		return gitCheckoutPathsReplacement(args)
	}
	mapped := map[string]any{}
	if len(args) > 0 && ((command == "checkout" && args[0] == "-b") || (command == "switch" && args[0] == "-c")) {
		mapped["create"] = true
		args = args[1:]
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return ReplacementCall{}, false
	}
	if command == "checkout" && mapped["create"] != true && confine.ProjectPathExists(args[0], projectDir) {
		return ReplacementCall{}, false
	}
	mapped["branch"] = args[0]
	args = args[1:]
	if len(args) == 1 && mapped["create"] == true && !strings.HasPrefix(args[0], "-") {
		mapped["ref"] = args[0]
		args = nil
	}
	if len(args) != 0 {
		return ReplacementCall{}, false
	}
	return ReplacementCall{Tool: "git_checkout", Args: mapped}, true
}

// gitCheckoutPathsReplacement maps `checkout [<tree-ish>] -- <paths>`: without a
// source it restores the worktree from the index; with one it writes both.
func gitCheckoutPathsReplacement(args []string) (ReplacementCall, bool) {
	separator := slices.Index(args, "--")
	source, paths := args[:separator], args[separator+1:]
	if len(source) > 1 || len(paths) == 0 {
		return ReplacementCall{}, false
	}
	for _, path := range paths {
		if !gitLiteralOperand(path) {
			return ReplacementCall{}, false
		}
	}
	mapped := map[string]any{"paths": stringsToAny(paths)}
	if len(source) == 1 {
		if strings.HasPrefix(source[0], "-") {
			return ReplacementCall{}, false
		}
		mapped["source"], mapped["staged"], mapped["worktree"] = source[0], true, true
	}
	return ReplacementCall{Tool: "git_restore", Args: mapped}, true
}

func gitMergeReplacement(args []string) (ReplacementCall, bool) {
	if len(args) == 1 && args[0] == "--abort" {
		return ReplacementCall{Tool: "git_merge", Args: map[string]any{"action": "abort"}}, true
	}
	if len(args) == 1 && args[0] == "--continue" {
		return ReplacementCall{Tool: "git_merge", Args: map[string]any{"action": "continue"}}, true
	}
	mapped := map[string]any{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--ff-only":
			mapped["mode"] = "ff_only"
		case "--ff":
			mapped["mode"] = "ff"
		case "--no-ff":
			mapped["mode"] = "no_ff"
		case "--no-commit":
			mapped["commit"] = false
		case "--commit":
			mapped["commit"] = true
		case "--no-edit":
		case "-m":
			i++
			if i >= len(args) {
				return ReplacementCall{}, false
			}
			mapped["message"] = args[i]
		default:
			if strings.HasPrefix(args[i], "-") || mapped["ref"] != nil {
				return ReplacementCall{}, false
			}
			mapped["ref"] = args[i]
		}
	}
	return ReplacementCall{Tool: "git_merge", Args: mapped}, mapped["ref"] != nil
}
func gitStashReplacement(args []string) (ReplacementCall, bool) {
	if len(args) == 1 && args[0] == "list" {
		return ReplacementCall{Tool: "git_stash_list", Args: map[string]any{}}, true
	}
	if len(args) < 2 {
		return ReplacementCall{}, false
	}
	action := args[0]
	mapped := map[string]any{}
	switch action {
	case "apply", "drop":
		mapped["action"] = action
		args = args[1:]
		if action == "apply" && len(args) > 0 && args[0] == "--index" {
			mapped["reinstate_index"] = true
			args = args[1:]
		}
		if len(args) != 1 || strings.HasPrefix(args[0], "-") {
			return ReplacementCall{}, false
		}
		mapped["ref"] = args[0]
	case "push":
		mapped["action"] = "save"
		paths := []any{}
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "-u", "--include-untracked":
				mapped["include_untracked"] = true
			case "-m", "--message":
				i++
				if i >= len(args) {
					return ReplacementCall{}, false
				}
				mapped["message"] = args[i]
			case "--":
				for _, path := range args[i+1:] {
					if !gitLiteralOperand(path) || filepath.IsAbs(path) || filepath.Clean(path) == "." || filepath.Clean(path) == ".." || strings.HasPrefix(filepath.Clean(path), ".."+string(filepath.Separator)) {
						return ReplacementCall{}, false
					}
					paths = append(paths, path)
				}
				i = len(args)
			default:
				return ReplacementCall{}, false
			}
		}
		if len(paths) == 0 {
			return ReplacementCall{}, false
		}
		mapped["paths"] = paths
	default:
		return ReplacementCall{}, false
	}
	return ReplacementCall{Tool: "git_stash", Args: mapped}, true
}

func gitLiteralOperand(path string) bool {
	return path != "" && !strings.HasPrefix(path, ":") && !strings.ContainsAny(path, "*?[\\")
}
