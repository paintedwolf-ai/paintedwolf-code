package contract

import (
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/toolvocab"
)

type programPattern struct {
	name string
	re   *regexp.Regexp
}

var (
	specificProgramPatterns = []programPattern{
		{"sed", regexp.MustCompile("`sed(?:\\s+-n[^`]*)?`|\\bsed\\s+-n\\b|(?:\\bwith|\\buse|,\\s*|\\bor\\s+)\\bsed\\b")},
		{"cat", regexp.MustCompile("`cat(?:\\s+-n)?`|\\bcat\\s+-n\\b")},
		{"head", regexp.MustCompile("`head`|\\bhead\\b(?:/`?tail`?|\\s*,\\s*tail\\b|\\s+for\\s+a\\s+window)")},
		{"tail", regexp.MustCompile("`tail`|`?head`?/`?tail`?|\\bhead,\\s*tail\\b|\\btail\\s+for\\s+a\\s+window")},
		{"grep", regexp.MustCompile("`grep\\s+-n`|\\bgrep\\s+-n\\b|,\\s*or\\s+grep\\b|\\bretry\\s+`?grep`?\\s+in\\s+command\\b")},
		{"rg", regexp.MustCompile("`rg\\s+-n`|\\brg\\s+-n\\b")},
		{"wc", regexp.MustCompile("`wc\\s+-l`|\\bwc\\s+-l\\b")},
		{"awk", regexp.MustCompile("`awk`|,\\s*awk,")},
		{"jq", regexp.MustCompile("`jq`|,\\s*or\\s+jq\\b")},
		{"jq-edit", regexp.MustCompile("`jq-edit\\b")},
		{"edit", regexp.MustCompile("`edit\\s+--path\\b")},
		{"replace-lines", regexp.MustCompile("`replace-lines\\b")},
		{"tee", regexp.MustCompile("`tee\\s+--syntax-override-reason\\b")},
		{"unzip", regexp.MustCompile("`unzip\\s+-p`")},
		{"tar", regexp.MustCompile("`tar\\s+-[tx]\\b")},
		{"git config", regexp.MustCompile("`git\\s+config\\b")},
		{"find", regexp.MustCompile("\\bretry\\s+`?find`?\\s+in\\s+command\\b")},
		{"repo-map", regexp.MustCompile("`repo-map\\b")},
		{"external", regexp.MustCompile("KEY='<reference>'")},
	}
	commandGenericRE  = regexp.MustCompile("(?i)\\b(?:use command|with command|inside command|in command|via command|through command|such as command|command redirect|command redirects)\\b|`command\\(")
	proseToolCallRE   = regexp.MustCompile("(?i)\\b(?:call|use|send|retry|run|invoke|prefer|reissue|submit)\\s+`?([a-z][a-z0-9_]*)\\b")
	backtickCallRE    = regexp.MustCompile("`([a-z][a-z0-9_]*)\\([^`\\n]*\\)`")
	loaderPromiseRE   = regexp.MustCompile(`(?i)\brequest_tools\b|\brequest it,\s*then run\b`)
	negativeCallRE    = regexp.MustCompile("(?i)\\b(?:do not|never|avoid|without|instead of|before|cannot|could not|did not)\\s+(?:call|calling|repeat|repeating|retry|retrying|use|using|run|running)?\\s*`?([a-z][a-z0-9_]*)\\b")
	histPrefixRE      = regexp.MustCompile(`(?i)(?:\bcannot|\bnot|\bthis|\banother|\bprior|\byour|\bthe|\ba|\ban)\s+$`)
	histSuffixRE      = regexp.MustCompile(`(?i)^\s*(?:is\s+blocked|was\s+blocked|scope\.|has\s+no|was\s+called|failed|refused|returned|cannot|could\s+not|did\s+not|is\s+not\s+offered)`)
	progNegBeforeRE   = regexp.MustCompile(`(?i)\b(?:do not|never|avoid|without|instead of|cannot|the|your|prior|called|ran)\b`)
	progImperativeRE  = regexp.MustCompile(`(?i)\b(?:use|with|via|run|prefer|inspect|through|such as|retry)\b`)
	prunedCmdFieldsRE = regexp.MustCompile("`?(capability_request|secret_use|snapshot_capture|socks_proxy|terminal_capture|verification)\\b")
)

var programCapabilityGuards = map[string]map[string]bool{
	"sed": {
		"paintedwolf.command_sed_reachable":          true,
		"paintedwolf.admitted_source_read_forms":     true,
		"paintedwolf.admitted_range_read_forms":      true,
		"paintedwolf.admitted_file_read_forms":       true,
		"paintedwolf.admitted_raw_read_forms":        true,
		"paintedwolf.admitted_large_read_forms":      true,
		"paintedwolf.admitted_read_addressing_forms": true,
		"paintedwolf.read_range_call":                true,
		"paintedwolf.admitted_stream_edit_forms":     true,
	},
	"cat": {
		"paintedwolf.admitted_source_read_forms": true,
		"paintedwolf.admitted_range_read_forms":  true,
		"paintedwolf.admitted_file_read_forms":   true,
		"paintedwolf.admitted_raw_read_forms":    true,
	},
	"head": {
		"paintedwolf.admitted_file_read_forms":       true,
		"paintedwolf.admitted_large_read_forms":      true,
		"paintedwolf.admitted_read_addressing_forms": true,
		"paintedwolf.read_window_calls":              true,
	},
	"tail": {
		"paintedwolf.admitted_large_read_forms":      true,
		"paintedwolf.admitted_read_addressing_forms": true,
		"paintedwolf.read_window_calls":              true,
	},
	"grep": {
		"paintedwolf.command_grep_reachable":     true,
		"paintedwolf.admitted_text_search_forms": true,
		"paintedwolf.admitted_large_read_forms":  true,
	},
	"rg": {
		"paintedwolf.admitted_text_search_forms":     true,
		"paintedwolf.admitted_read_addressing_forms": true,
		"paintedwolf.read_symbol_call":               true,
	},
	"wc":            {"paintedwolf.command_wc_reachable": true},
	"awk":           {"paintedwolf.admitted_stream_edit_forms": true},
	"jq":            {"paintedwolf.admitted_stream_edit_forms": true},
	"jq-edit":       {"paintedwolf.command_jq_edit_reachable": true, "paintedwolf.secret_jq_edit_call": true},
	"edit":          {"paintedwolf.admitted_mutation_forms": true, "paintedwolf.admitted_secret_config_forms": true},
	"replace-lines": {"paintedwolf.admitted_mutation_forms": true, "paintedwolf.admitted_secret_config_forms": true},
	"tee":           {"paintedwolf.command_tee_reachable": true},
	"unzip":         {"paintedwolf.admitted_archive_forms": true, "paintedwolf.archive_unzip_stream_call": true},
	"tar": {
		"paintedwolf.admitted_archive_forms":   true,
		"paintedwolf.archive_tar_list_call":    true,
		"paintedwolf.archive_tar_extract_call": true,
	},
	"git config": {"paintedwolf.command_git_config_reachable": true},
	"find":       {"paintedwolf.command_find_reachable": true},
	"repo-map":   {"paintedwolf.command_repo_map_reachable": true},
	"external":   {"paintedwolf.command_external_reachable": true},
}

func buildGenericCommandGuards() map[string]bool {
	out := map[string]bool{
		"paintedwolf.can_command":                 true,
		"paintedwolf.command_operation_reachable": true,
	}
	for _, m := range programCapabilityGuards {
		for k := range m {
			out[k] = true
		}
	}
	return out
}

var genericCommandGuards = buildGenericCommandGuards()

func promisedActionsInText(vocab *toolvocab.Catalog, text string) (needsGenericCmd bool, programs, tools []string) {
	clean := regexp.MustCompile(`\{\{.*?\}\}`).ReplaceAllString(text, " ")
	progSeen := map[string]bool{}
	for _, pat := range specificProgramPatterns {
		for _, idx := range pat.re.FindAllStringIndex(clean, -1) {
			before := clean[max(0, idx[0]-40):idx[0]]
			tail := before[max(0, len(before)-20):]
			after := clean[idx[1]:min(len(clean), idx[1]+35)]
			if progNegBeforeRE.MatchString(before) && !progImperativeRE.MatchString(tail) {
				continue
			}
			if histSuffixRE.MatchString(after) {
				continue
			}
			if !progSeen[pat.name] {
				progSeen[pat.name] = true
				programs = append(programs, pat.name)
			}
			break
		}
	}
	needsGenericCmd = commandGenericRE.MatchString(clean)
	negated := map[string]bool{}
	for _, m := range negativeCallRE.FindAllStringSubmatch(clean, -1) {
		negated[strings.ToLower(m[1])] = true
	}
	toolSeen := map[string]bool{}
	addTool := func(name string) {
		name = strings.ToLower(name)
		if name == "" || name == "command" || negated[name] || progSeen[name] || toolSeen[name] {
			return
		}
		if vocab != nil && !vocab.KnownName(name) {
			return
		}
		if strings.Contains(clean, "prior "+name) || strings.Contains(clean, "from "+name) || strings.Contains(clean, name+" is not offered") {
			return
		}
		toolSeen[name] = true
		tools = append(tools, name)
	}
	for _, idx := range backtickCallRE.FindAllStringSubmatchIndex(clean, -1) {
		before := clean[max(0, idx[0]-30):idx[0]]
		after := clean[idx[1]:min(len(clean), idx[1]+35)]
		if histSuffixRE.MatchString(after) || histPrefixRE.MatchString(before) {
			continue
		}
		name := strings.ToLower(clean[idx[2]:idx[3]])
		if name == "command" {
			needsGenericCmd = true
			continue
		}
		addTool(name)
	}
	for _, idx := range proseToolCallRE.FindAllStringSubmatchIndex(clean, -1) {
		if idx[1] < len(clean) && clean[idx[1]] == '-' {
			continue
		}
		before := clean[max(0, idx[0]-25):idx[0]]
		if histPrefixRE.MatchString(before) {
			continue
		}
		addTool(clean[idx[2]:idx[3]])
	}
	slices.Sort(programs)
	slices.Sort(tools)
	return needsGenericCmd, programs, tools
}
