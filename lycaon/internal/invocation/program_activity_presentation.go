package invocation

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// Phase is the typed fold and headline key; group is its display name.
type activityRule struct{ role, group, label, phase string }

// Classification precedence: containers and launches, codebase mapping and
// summarization, version control, file mutations, network and process control,
// read-only file work, then environment inspection and pipeline output.
func activityPresentation(input activityInput) activityRule {
	receipt := input.receipt
	cmd := receipt.NativeCommand
	if cmd == "" {
		cmd = input.operation
	}
	isReadOnly := receipt.Lifecycle == "read_only" || hasActivityClass(receipt, "read_only")
	isMutation := hasActivityClass(receipt, "mutation")
	if input.operation == "program &" {
		return activityRule{"primary", "Running programs", "Start background job", "execute"}
	}
	if strings.HasPrefix(input.operation, "program ") {
		return activityRule{"container", "Running commands", "Run command", "execute"}
	}
	if receipt.EffectMode == "external" || cmd == "external" || (input.operation == "command" && receipt.Owner == "processes") {
		return activityRule{"primary", "Running programs", "Run process", "execute"}
	}
	if input.operation == "cd" || cmd == "cd" {
		return activityRule{"context", "Inspecting environment", "Change directory", "inspect"}
	}
	if input.operation == "survey_repo" || cmd == "repo-map" || cmd == "survey-repo" {
		return activityRule{"primary", "Mapping codebase", "Map repository", "inspect"}
	}
	if cmd == "outline" {
		return activityRule{"primary", "Mapping codebase", "Outline symbols", "inspect"}
	}
	if input.operation == "summarize" || cmd == "summarize" {
		return activityRule{"primary", "Summarizing source", "Summarize source", "inspect"}
	}
	if strings.HasPrefix(input.operation, "git") || input.family == "git" {
		switch {
		case cmd == "git grep":
			return activityRule{"primary", "Searching codebase", "Search text", "inspect"}
		case cmd == "git fetch" || cmd == "git pull" || cmd == "git push" || cmd == "git clone" || cmd == "git ls-remote":
			return activityRule{"primary", "Syncing Git remote", "Sync Git remote", "execute"}
		case input.operation == "git_commit" || cmd == "git commit":
			return activityRule{"primary", "Recording Git changes", "Create commit", "mutate"}
		case (input.operation == "git_add" || cmd == "git add" || cmd == "git rm" || cmd == "git mv") && !isReadOnly:
			return activityRule{"primary", "Recording Git changes", "Stage changes", "mutate"}
		case (input.operation == "git_checkout" || cmd == "git checkout" || cmd == "git switch" || cmd == "git worktree") && !isReadOnly:
			return activityRule{"primary", "Managing Git branches", "Switch branch", "mutate"}
		case (input.operation == "git_merge" || cmd == "git merge" || cmd == "git rebase" || cmd == "git cherry-pick" || cmd == "git apply") && !isReadOnly:
			return activityRule{"primary", "Managing Git branches", "Merge or rebase", "mutate"}
		case (input.operation == "git_restore" || input.operation == "git_stash" || cmd == "git restore" || cmd == "git stash" || cmd == "git reset" || cmd == "git revert" || cmd == "git clean") && !isReadOnly:
			return activityRule{"primary", "Managing Git branches", "Stash or restore", "mutate"}
		case !isReadOnly:
			return activityRule{"primary", "Recording Git changes", "Change Git state", "mutate"}
		case input.operation == "git_status" || cmd == "git status":
			return activityRule{"primary", "Inspecting Git", "Check Git status", "inspect"}
		case input.operation == "git_diff" || input.operation == "git_show" || cmd == "git diff" || cmd == "git show" || cmd == "git cat-file" || cmd == "git apply":
			return activityRule{"primary", "Inspecting Git", "Compare Git changes", "inspect"}
		case input.operation == "git_log" || input.operation == "git_blame" || cmd == "git log" || cmd == "git blame" || cmd == "git reflog" || cmd == "git shortlog" || strings.HasPrefix(cmd, "git bisect"):
			return activityRule{"primary", "Inspecting Git", "Read Git history", "inspect"}
		default:
			return activityRule{"primary", "Inspecting Git", "List Git refs", "inspect"}
		}
	}
	if isMutation {
		switch {
		case input.operation == "code_rewrite" || cmd == "code-rewrite":
			return activityRule{"primary", "Editing files", "Rewrite code", "mutate"}
		case input.operation == "jq_edit" || cmd == "jq-edit":
			return activityRule{"primary", "Editing files", "Update data file", "mutate"}
		case input.operation == "edit" || input.operation == "replace_lines" || cmd == "edit" || cmd == "replace-lines" || cmd == "patch" || cmd == "sed":
			return activityRule{"primary", "Editing files", "Edit file", "mutate"}
		case input.operation == "write" || cmd == "write" || cmd == "tee" || cmd == "touch":
			return activityRule{"primary", "Editing files", "Write file", "mutate"}
		case input.family == "archive" || input.operation == "extract_archive" || cmd == "tar" || cmd == "unzip" || cmd == "zip" || cmd == "gzip" || cmd == "gunzip":
			return activityRule{"primary", "Archiving files", "Extract or archive", "mutate"}
		case input.operation == "mkdir" || cmd == "mkdir" || cmd == "mktemp":
			return activityRule{"primary", "Managing files", "Create directory", "mutate"}
		case input.operation == "move" || input.operation == "copy" || cmd == "mv" || cmd == "cp" || cmd == "ln":
			return activityRule{"primary", "Managing files", "Move or copy files", "mutate"}
		case input.operation == "delete" || cmd == "rm" || cmd == "rmdir":
			return activityRule{"primary", "Managing files", "Remove files", "mutate"}
		case input.operation == "chmod" || input.operation == "chown" || cmd == "chmod" || cmd == "chown":
			return activityRule{"primary", "Managing files", "Change permissions", "mutate"}
		default:
			return activityRule{"primary", "Editing files", "Write file", "mutate"}
		}
	}
	if hasActivityClass(receipt, "network_egress") || input.operation == "http_request" || cmd == "curl" {
		return activityRule{"primary", "Accessing the network", "Fetch URL", "execute"}
	}
	if input.operation == "process_signal" || cmd == "kill" || cmd == "pkill" {
		return activityRule{"primary", "Controlling processes", "Stop process", "execute"}
	}
	if cmd == "wait" || cmd == "sleep" || cmd == "timeout" {
		return activityRule{"primary", "Controlling processes", "Wait for process", "execute"}
	}
	reads := activityPaths(input.arguments, "read")
	switch {
	case input.operation == "find" || input.operation == "list_dir" || cmd == "find" || cmd == "ls" || cmd == "tree":
		return activityRule{"primary", "Searching codebase", "Find files", "inspect"}
	case input.operation == "grep" || cmd == "grep" || cmd == "rg" || (input.family == "search" && len(reads) > 0):
		return activityRule{"primary", "Searching codebase", "Search text", "inspect"}
	case (input.operation == "jq" || cmd == "jq" || cmd == "yq" || input.family == "query") && len(reads) > 0 && !input.pipeline:
		return activityRule{"primary", "Searching codebase", "Query data", "inspect"}
	case input.operation == "diff" || cmd == "diff" || cmd == "cmp" || (cmd == "comm" && len(reads) > 0):
		return activityRule{"primary", "Comparing files", "Compare files", "inspect"}
	case (input.family == "archive" || cmd == "tar" || cmd == "unzip" || cmd == "zip") && cmd != "zcat":
		return activityRule{"primary", "Checking file metadata", "List archive", "inspect"}
	case input.operation == "stat" || cmd == "stat" || cmd == "du" || cmd == "df" || cmd == "readlink" || cmd == "realpath" || cmd == "md5sum" || cmd == "sha1sum" || cmd == "sha256sum" || cmd == "sha512sum" || cmd == "shasum" || ((input.operation == "wc" || cmd == "wc") && len(reads) > 0 && !input.pipeline):
		return activityRule{"primary", "Checking file metadata", "Check file info", "inspect"}
	case (cmd == "hexdump" || cmd == "od" || cmd == "xxd" || cmd == "base64" || cmd == "file") && len(reads) > 0 && !input.pipeline:
		return activityRule{"primary", "Reading files", "Inspect binary", "inspect"}
	case (input.family == "inspect" || input.family == "sed" || isReadOnly) && len(reads) > 0:
		return activityRule{"primary", "Reading files", "Read file", "inspect"}
	}
	if input.operation == "process_list" || cmd == "ps" || cmd == "pgrep" || cmd == "lsof" || cmd == "jobs" {
		return activityRule{"primary", "Inspecting processes", "Check processes", "inspect"}
	}
	if !input.pipeline && (cmd == "pwd" || cmd == "env" || cmd == "printenv" || cmd == "uname" || cmd == "hostname" || cmd == "whoami" || cmd == "id" || cmd == "nproc" || cmd == "ulimit" || cmd == "which" || cmd == "type" || cmd == "date") {
		return activityRule{"primary", "Inspecting environment", "Check environment", "inspect"}
	}
	if isReadOnly {
		if input.pipeline || len(reads) == 0 {
			return activityRule{"support", "Processing output", "Filter output", "inspect"}
		}
		return activityRule{"primary", "Processing output", "Filter output", "inspect"}
	}
	return activityRule{"primary", "Running commands", "Run command", "execute"}
}

// Steps read as actions: label with subject when the operation has one.
func activityStepLabel(g activityGroup) string {
	rule := activityPresentation(g.input)
	if subject := activitySubject(g.input); subject != "" {
		return boundedActivityText(rule.label + " · " + subject)
	}
	if command := g.input.receipt.Command; command != "" {
		return boundedActivityText(command)
	}
	return rule.label
}

func hasActivityClass(receipt api.CommandExecutionReceipt, class string) bool {
	for _, value := range receipt.EffectClasses {
		if value == class {
			return true
		}
	}
	return false
}

func stringArgument(arguments map[string]any, name string) string {
	value, _ := arguments[name].(string)
	return value
}

func activityPaths(arguments map[string]any, role string) []string {
	targets, _ := arguments["path_targets"].(map[string]any)
	paths, _ := targets[role].([]any)
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if value, ok := path.(string); ok {
			result = append(result, value)
		}
	}
	if len(result) == 0 && role == "read" {
		if path := stringArgument(arguments, "path"); path != "" {
			result = append(result, path)
		}
	}
	return result
}

func activitySubject(input activityInput) string {
	if input.operation == "program &" {
		return ""
	}
	if input.operation == "summarize" || input.receipt.NativeCommand == "summarize" {
		if subject, ok := summarizeActivitySubject(input); ok {
			return subject
		}
	}
	if input.operation == "survey_repo" || input.receipt.NativeCommand == "repo-map" || input.receipt.NativeCommand == "survey-repo" {
		if subject, ok := surveyActivitySubject(input); ok {
			return subject
		}
	}
	if query := stringArgument(input.arguments, "query"); query != "" {
		return boundedActivityText(query)
	}
	if pattern := stringArgument(input.arguments, "pattern"); pattern != "" {
		return boundedActivityText(pattern)
	}
	if url := stringArgument(input.arguments, "url"); url != "" {
		return boundedActivityText(url)
	}
	if message := stringArgument(input.arguments, "message"); message != "" {
		return boundedActivityText(message)
	}
	if activityPresentation(input).label == "Run process" {
		if command := stringArgument(input.arguments, "command"); command != "" {
			return boundedActivityText(command)
		}
	}
	for _, role := range []string{"write", "read"} {
		if paths := activityPaths(input.arguments, role); len(paths) > 0 {
			return boundedActivityText(pathSummary(paths))
		}
	}
	return boundedActivityText(input.receipt.Command)
}

// pathSummary names one path, or the first of many.
func pathSummary(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	if len(paths) == 1 {
		return paths[0]
	}
	return fmt.Sprintf("%s · multiple paths", paths[0])
}

// Summarize subjects lead with the summarized path and the requested focus.
func summarizeActivitySubject(input activityInput) (string, bool) {
	focus := stringArgument(input.arguments, "focus")
	if focus == "" {
		focus = stringArgument(input.arguments, "task")
	}
	reads := activityPaths(input.arguments, "read")
	switch {
	case len(reads) > 0 && focus != "":
		return boundedActivityText(fmt.Sprintf("%s — %s", pathSummary(reads), focus)), true
	case len(reads) > 0:
		return boundedActivityText(pathSummary(reads)), true
	case focus != "":
		return boundedActivityText(focus), true
	}
	return "", false
}

// Survey subjects lead with the mapped path and the requested bundle.
func surveyActivitySubject(input activityInput) (string, bool) {
	bundle := stringArgument(input.arguments, "bundle")
	reads := activityPaths(input.arguments, "read")
	switch {
	case len(reads) > 0 && bundle != "":
		return boundedActivityText(fmt.Sprintf("%s · %s", pathSummary(reads), bundle)), true
	case len(reads) > 0:
		return boundedActivityText(pathSummary(reads)), true
	case bundle != "":
		return boundedActivityText(bundle), true
	}
	return "", false
}

func boundedActivityText(value string) string {
	const maximum = 256
	runes := []rune(value)
	if len(runes) > maximum {
		return string(runes[:maximum]) + "…"
	}
	return value
}
