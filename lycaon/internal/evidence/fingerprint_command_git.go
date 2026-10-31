package evidence

import (
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
)

var (
	commandOpenPathRE     = regexp.MustCompile(`(?i)open\s*\(\s*['"]([^'"]+)['"]`)
	commandFileTailPathRE = regexp.MustCompile(`^([^\s:]+\.[A-Za-z0-9]+(?:\.[A-Za-z0-9]+)*):`)
	commandPathVerbs      = map[string]struct{}{"file": {}, "cat": {}, "head": {}, "tail": {}, "stat": {}, "wc": {}, "sed": {}, "less": {}}
	gitJSONPathRE         = regexp.MustCompile(`"path"\s*:\s*"([^"]+)"`)
	gitDiffHeaderRE       = regexp.MustCompile(`(?:diff --git |[-+]{3} |rename (?:from|to) )([ab]/[^\s"\\]+)`)
)

// populateCommandRecord captures output and indexes path-like tokens.
func populateCommandRecord(projectDir string, rec *Record, content string) {
	if rec == nil {
		return
	}
	body := captureRecordBody(rec, content)
	if obj, ok := jsonToolPayload(body); ok && isCommandResultPayload(obj) {
		populateCommandJSONRecord(projectDir, rec, obj)
		return
	}
	scanCommandPaths(projectDir, rec, body, nil)
}

func isCommandResultPayload(obj map[string]any) bool {
	if _, ok := obj["ok"]; !ok {
		return false
	}
	if _, ok := obj["command"]; ok {
		return true
	}
	stages, ok := obj["stages"].([]any)
	return ok && len(stages) > 0
}

func populateCommandJSONRecord(projectDir string, rec *Record, obj map[string]any) {
	cmd := commandFromResult(obj)
	tail, _ := obj["tail"].(string)
	for _, p := range pathsFromShellCommand(cmd) {
		addRecordPath(projectDir, rec, p, FidelityScraped)
	}
	if p := pathFromFileCommandTail(tail); p != "" {
		addRecordPath(projectDir, rec, p, FidelityScraped)
	}
	if strings.TrimSpace(tail) != "" {
		scanCommandPaths(projectDir, rec, tail, nil)
	}
}

func commandFromResult(obj map[string]any) string {
	if command, ok := obj["command"].(string); ok && strings.TrimSpace(command) != "" {
		return strings.TrimSpace(command)
	}
	stages, ok := obj["stages"].([]any)
	if !ok || len(stages) == 0 {
		return ""
	}
	first, ok := stages[0].(map[string]any)
	if !ok {
		return ""
	}
	command, _ := first["command"].(string)
	return strings.TrimSpace(command)
}

func pathsFromShellCommand(command string) []string {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(raw string) {
		raw = strings.Trim(raw, `"'`)
		raw = NormalizeLedgerPath(raw)
		if raw == "" || sandbox.HasParentTraversal(raw) {
			return
		}
		if !looksLikeShellPathToken(raw) {
			return
		}
		if _, dup := seen[raw]; dup {
			return
		}
		seen[raw] = struct{}{}
		out = append(out, raw)
	}
	for _, m := range commandOpenPathRE.FindAllStringSubmatch(command, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	fields := strings.Fields(command)
	for i, field := range fields {
		verb := strings.ToLower(strings.Trim(field, `"'`))
		if _, ok := commandPathVerbs[verb]; !ok || i+1 >= len(fields) {
			continue
		}
		add(fields[i+1])
	}
	return out
}

func pathFromFileCommandTail(tail string) string {
	tail = strings.TrimSpace(tail)
	if tail == "" {
		return ""
	}
	m := commandFileTailPathRE.FindStringSubmatch(tail)
	if len(m) < 2 {
		return ""
	}
	path := NormalizeLedgerPath(m[1])
	if path == "" || sandbox.HasParentTraversal(path) || !looksLikeShellPathToken(path) {
		return ""
	}
	return path
}

// populateGitRecord indexes paths from Git receipts and embedded diffs.
func populateGitRecord(projectDir string, rec *Record, content string) {
	if rec == nil {
		return
	}
	body := captureRecordBody(rec, content)
	for _, p := range gitPathsFromOutput(body) {
		addRecordPath(projectDir, rec, p, FidelityStructured)
	}
}

// gitPathsFromOutput extracts repo-relative paths from a git tool's JSON body — stat-mode
// files[], structured "path" fields, and the a/ and b/ headers of any embedded unified diff —
// with git's a/ b/ prefixes stripped and /dev/null dropped.
func gitPathsFromOutput(body string) []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(raw string) {
		p := stripGitPathPrefix(strings.TrimSpace(raw))
		if p == "" || sandbox.HasParentTraversal(p) {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if obj, ok := jsonToolPayload(body); ok {
		if paths, ok := obj["paths"].([]any); ok {
			for _, raw := range paths {
				if path, ok := raw.(string); ok {
					add(path)
				}
			}
		}
		if raw, ok := obj["files"].([]any); ok {
			for _, item := range raw {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if p, ok := m["path"].(string); ok {
					add(p)
				}
			}
		}
	}
	for _, m := range gitJSONPathRE.FindAllStringSubmatch(body, -1) {
		add(m[1])
	}
	for _, m := range gitDiffHeaderRE.FindAllStringSubmatch(body, -1) {
		add(m[1])
	}
	return out
}

// scanCommandPaths indexes path tokens after an optional prefix transformation.
func scanCommandPaths(projectDir string, rec *Record, text string, transform func(string) string) {
	if rec == nil || strings.TrimSpace(text) == "" {
		return
	}
	seen := map[string]struct{}{}
	for _, field := range strings.Fields(text) {
		field = strings.Trim(field, `"'`)
		if transform != nil {
			field = transform(field)
		}
		if !looksLikeShellPathToken(field) {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		addRecordPath(projectDir, rec, field, FidelityScraped)
	}
}

// stripGitPathPrefix drops git's a/ and b/ diff path prefixes and discards /dev/null.
func stripGitPathPrefix(token string) string {
	token = strings.Trim(token, `"'`)
	if token == "/dev/null" { //nolint:gosec // G101 — POSIX null-device path git uses for added/deleted sides, not a secret
		return ""
	}
	if strings.HasPrefix(token, "a/") || strings.HasPrefix(token, "b/") {
		return token[2:]
	}
	return token
}
