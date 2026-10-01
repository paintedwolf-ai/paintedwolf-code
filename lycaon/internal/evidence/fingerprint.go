package evidence

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/internal/sandbox"
)

var (
	toolBodyURLRE         = regexp.MustCompile(`https?://[^\s\])>'"]+`)
	commandOpenPathRE     = regexp.MustCompile(`(?i)open\s*\(\s*['"]([^'"]+)['"]`)
	commandFileTailPathRE = regexp.MustCompile(`^([^\s:]+\.[A-Za-z0-9]+(?:\.[A-Za-z0-9]+)*):`)
	commandPathVerbs      = map[string]struct{}{"file": {}, "cat": {}, "head": {}, "tail": {}, "stat": {}, "wc": {}, "sed": {}, "less": {}}
)

// BuildEvidenceRecord constructs one evidence row from a captured tool result.
func BuildEvidenceRecord(projectDir, toolName string, args map[string]any, content string) Record {
	rec := buildEvidenceRecord(projectDir, toolName, args, content)
	if rec.Kind == "" {
		return Record{}
	}
	if toolName == "read" {
		rec.Survey = ReadEvidenceSurvey(args, content)
	}
	if toolName == "list_dir" {
		rec.Survey = ListEvidenceSurvey(args, content)
	}
	if toolName == "grep" {
		rec.Survey = GrepEvidenceSurvey(args, content)
	}
	if toolName == "find" {
		rec.Survey = FindEvidenceSurvey(args, content)
	}
	return rec
}

func indexRecordPaths(ev *Ledger, handle string, rec Record) {
	for _, path := range IndexedPathsForRecord(rec) {
		indexLedgerPath(ev, handle, path, pathTrustOnRecord(rec, path))
	}
}

func buildEvidenceRecord(projectDir, toolName string, args map[string]any, content string) Record {
	toolName = strings.TrimSpace(strings.ToLower(toolName))
	binding := ActiveBinding()
	kind := binding.ToolKindForArgs(toolName, args)
	if kind == "" {
		return Record{}
	}
	shape := binding.ShapeForToolKind(toolName, kind)
	rec := Record{Kind: kind, Shape: shape, SourceTool: toolName, Survey: ActiveBinding().IsSurveyKind(kind)}
	if ingestion.IsMCPToolName(toolName) {
		rec.Fidelity = FidelityOpaque
		populateMCPEvidenceRecord(projectDir, &rec, content)
		return rec
	}
	switch toolName {
	case "read":
		rec.Fidelity = FidelityStructured
		populateReadRecord(projectDir, &rec, args, content)
	case "grep":
		rec.Fidelity = FidelityStructured
		populateGrepRecord(projectDir, &rec, args, content)
	case "find":
		addRecordPath(projectDir, &rec, stringArg(args, "path"), FidelityStructured)
		for _, p := range findResultPaths(content) {
			addRecordPath(projectDir, &rec, p, FidelityStructured)
		}
	case "list_dir":
		addRecordPath(projectDir, &rec, stringArg(args, "path"), FidelityStructured)
		for _, p := range listDirPaths(content) {
			addRecordPath(projectDir, &rec, p, FidelityStructured)
		}
	case "write", "edit", "replace_lines", "restore_version":
		addRecordPath(projectDir, &rec, stringArg(args, "path"), FidelityStructured)
	case "jq_edit":
		target := stringArg(args, "dest")
		if target == "" {
			target = stringArg(args, "path")
		}
		addRecordPath(projectDir, &rec, target, FidelityStructured)
	case "code_rewrite":
		for _, p := range declaredPathsFromArgs(args) {
			addRecordPath(projectDir, &rec, p, FidelityStructured)
		}
	case "delete":
		for _, p := range declaredPathsFromArgs(args) {
			addRecordPath(projectDir, &rec, p, FidelityStructured)
		}
		for _, p := range deleteResultPaths(content) {
			addRecordPath(projectDir, &rec, p, FidelityStructured)
		}
	case "stat", "wc":
		// Counts and metadata verify against the captured result body.
		addRecordPath(projectDir, &rec, stringArg(args, "path"), FidelityStructured)
		captureRecordBody(&rec, content)
	case "command":
		if rec.Shape == ShapeSurfaceSnapshot {
			rec.Fidelity = FidelityStructured
			rec.Surface = SurfaceTUI
			captureRecordBody(&rec, content)
		} else {
			rec.Fidelity = FidelityScraped
			populateCommandRecord(projectDir, &rec, content)
		}
	case "source_history":
		// History claims verify against actor-classified records.
		rec.Fidelity = FidelityStructured
		addRecordPath(projectDir, &rec, stringArg(args, "path"), FidelityStructured)
		captureRecordBody(&rec, content)
	case "git_status", "git_diff", "git_log", "git_show", "git_blame", "git_branches", "git_ref", "git_compare", "git_stash_list",
		"git_commit", "git_restore", "git_checkout", "git_merge", "git_stash":
		// Git results can index unreadable paths.
		rec.Fidelity = FidelityStructured
		addRecordPath(projectDir, &rec, stringArg(args, "path"), FidelityStructured)
		populateGitRecord(projectDir, &rec, content)
	case "web_search":
		// Every returned URL is an observed search result.
		rec.Fidelity = FidelityStructured
		populateSearchRecord(&rec, content)
	case "fetch_url":
		// Embedded links are not observed pages.
		rec.Fidelity = FidelityStructured
		rec.touchURL(stringArg(args, "url"))
		populateFetchTitle(&rec, stringArg(args, "url"), content)
		if dest := stringArg(args, "dest"); dest != "" {
			addRecordPath(projectDir, &rec, dest, FidelityStructured)
		}
	case "diff":
		// Diff evidence binds both inputs to the captured body.
		addRecordPath(projectDir, &rec, stringArg(args, "path_a"), FidelityStructured)
		addRecordPath(projectDir, &rec, stringArg(args, "path_b"), FidelityStructured)
		captureRecordBody(&rec, content)
	case "scan_pack", "scan_list", "scan_summary", "scan_query", "scan_compare":
		rec.Fidelity = FidelityStructured
		populateScanArtifactRecord(projectDir, &rec, content)
	case "survey_repo":
		rec.Fidelity = FidelityStructured
		rec.Survey = true
		populateSurveyRepoRecord(projectDir, &rec, args, content)
	case "summarize":
		rec.Fidelity = FidelityStructured
		populateSummarizeRecord(projectDir, &rec, content)
		populateSummarizePackRecord(projectDir, &rec, content)
	case "recall":
		// Recall observes index results, not file contents.
		rec.Fidelity = FidelityStructured
		rec.Survey = true
		populateRecallRecord(projectDir, &rec, content)
	case "skills_read":
		// Skill paths are relative to the skill directory.
		rec.Fidelity = FidelityStructured
		captureRecordBody(&rec, content)
	case "render_view":
		// Authored mockups do not prove runtime state.
		rec.Fidelity = FidelityStructured
		captureRecordBody(&rec, content)
	case "capture_page", "page_snapshot", "page_open":
		rec.Fidelity = FidelityStructured
		rec.Surface = SurfaceDOM
		rec.touchURL(stringArg(args, "url"))
		populateURLFromPayload(&rec, content)
		captureRecordBody(&rec, content)
	case "terminal_snapshot":
		rec.Fidelity = FidelityStructured
		rec.Surface = SurfaceTUI
		captureRecordBody(&rec, content)
	case "measure_page":
		rec.Fidelity = FidelityStructured
		rec.touchURL(stringArg(args, "url"))
		populateURLFromPayload(&rec, content)
		captureRecordBody(&rec, content)
	case "secret_generate", "secret_list", "secret_revoke":
		// Secret lifecycle results contain metadata and managed references only.
		rec.Fidelity = FidelityStructured
		captureRecordBody(&rec, content)
	case "http_request":
		rec.Fidelity = FidelityStructured
		rec.Surface = SurfaceHTTP
		rec.touchURL(stringArg(args, "url"))
		populateURLFromPayload(&rec, content)
		captureRecordBody(&rec, content)
	case "terminal_open", "terminal_send", "terminal_read", "terminal_close":
		rec.Fidelity = FidelityStructured
		rec.Surface = SurfaceTUI
		captureRecordBody(&rec, content)
	}
	return rec
}

func addRecordPath(projectDir string, rec *Record, token, tier string) {
	if rec != nil {
		rec.touchPath(projectDir, token, tier)
	}
}

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

// populateScanArtifactRecord captures scan tool JSON and indexes finding paths.
func populateScanArtifactRecord(projectDir string, rec *Record, content string) {
	if rec == nil {
		return
	}
	body := captureRecordBody(rec, content)
	for _, p := range scanPathsFromArtifactJSON(body) {
		addRecordPath(projectDir, rec, p, FidelityStructured)
	}
}

// scanPathsFromArtifactJSON walks the real scan tool wire shapes (ScanQueryResponse,
// ScanPackToolResult, CodeScan, ListScansResponse) and returns repo-relative finding paths.
func scanPathsFromArtifactJSON(body string) []string {
	obj, ok := jsonToolPayload(body)
	if !ok {
		return scanJSONFilePaths(body)
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(raw string) {
		p := NormalizeLedgerPath(raw)
		if p == "" || sandbox.HasParentTraversal(p) {
			return
		}
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	collectScanArtifactPaths(obj, add)
	return out
}

func collectScanArtifactPaths(obj map[string]any, add func(string)) {
	if obj == nil {
		return
	}
	collectScanGuidancePaths(obj["guidance"], add)
	collectScanGuidancePaths(obj["guidance_preview"], add)
	collectScanFindingPaths(obj["findings"], add)
	collectScanFindingPaths(obj["new_findings"], add)
	collectScanFindingPaths(obj["resolved_findings"], add)
	collectScanFindingPaths(obj["persisted_findings"], add)
	collectScanFindingPaths(obj["new_findings_sample"], add)
	if perScan, ok := obj["per_scan"].([]any); ok {
		for _, row := range perScan {
			if m, ok := row.(map[string]any); ok {
				collectScanGuidancePaths(m["guidance_preview"], add)
			}
		}
	}
	if scans, ok := obj["scans"].([]any); ok {
		for _, row := range scans {
			if m, ok := row.(map[string]any); ok {
				collectScanArtifactPaths(m, add)
			}
		}
	}
}

func collectScanGuidancePaths(raw any, add func(string)) {
	arr, ok := raw.([]any)
	if !ok {
		return
	}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if f, ok := m["file"].(string); ok {
			add(f)
		}
	}
}

func collectScanFindingPaths(raw any, add func(string)) {
	arr, ok := raw.([]any)
	if !ok {
		return
	}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		locs, ok := m["locations"].([]any)
		if !ok {
			continue
		}
		for _, loc := range locs {
			lm, ok := loc.(map[string]any)
			if !ok {
				continue
			}
			if uri, ok := lm["uri"].(string); ok {
				add(uri)
			}
		}
	}
}

func scanJSONFilePaths(body string) []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(raw string) {
		p := NormalizeLedgerPath(raw)
		if p == "" || sandbox.HasParentTraversal(p) {
			return
		}
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, m := range scanFindingFileRE.FindAllStringSubmatch(body, -1) {
		add(m[1])
	}
	for _, m := range scanFindingURIRE.FindAllStringSubmatch(body, -1) {
		add(m[1])
	}
	for _, m := range gitJSONPathRE.FindAllStringSubmatch(body, -1) {
		add(m[1])
	}
	return out
}

func populateSurveyRepoRecord(projectDir string, rec *Record, args map[string]any, content string) {
	if rec == nil {
		return
	}
	captureRecordBody(rec, content)
	addRecordPath(projectDir, rec, stringArg(args, "path"), FidelityStructured)
	obj, ok := jsonToolPayload(content)
	if !ok {
		return
	}
	if p, ok := obj["path"].(string); ok {
		addRecordPath(projectDir, rec, p, FidelityStructured)
	}
	raw, ok := obj["snapshot"].([]any)
	if !ok {
		return
	}
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if p, ok := m["path"].(string); ok {
			addRecordPath(projectDir, rec, p, FidelityStructured)
		}
	}
}

var (
	gitJSONPathRE     = regexp.MustCompile(`"path"\s*:\s*"([^"]+)"`)
	gitDiffHeaderRE   = regexp.MustCompile(`(?:diff --git |[-+]{3} |rename (?:from|to) )([ab]/[^\s"\\]+)`)
	scanFindingFileRE = regexp.MustCompile(`"file"\s*:\s*"([^"]+)"`)
	scanFindingURIRE  = regexp.MustCompile(`"uri"\s*:\s*"([^"]+)"`)
)

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

// captureRecordBody retains bounded tool output without trailing host feedback.
func captureRecordBody(rec *Record, content string) string {
	body := stripToolHostSuffix(content)
	if len(body) > EvidenceCaptureBodyCapBytes {
		body = body[:EvidenceCaptureBodyCapBytes]
		rec.Truncated = true
	}
	if strings.TrimSpace(body) != "" {
		rec.Body = append(rec.Body, body)
	}
	return body
}

func populateMCPEvidenceRecord(projectDir string, rec *Record, content string) {
	if rec == nil {
		return
	}
	body := captureRecordBody(rec, content)
	for _, url := range urlsInToolBody(body) {
		rec.touchURL(url)
	}
	scanCommandPaths(projectDir, rec, body, nil)
}

func populateSearchRecord(rec *Record, content string) {
	if rec == nil {
		return
	}
	body := stripToolHostSuffix(content)
	for _, url := range urlsInToolBody(body) {
		rec.touchURL(url)
	}
	// Search titles supply citation labels.
	obj, ok := jsonToolPayload(content)
	if !ok {
		return
	}
	results, _ := obj["results"].([]any)
	for _, item := range results {
		hit, ok := item.(map[string]any)
		if !ok {
			continue
		}
		url, _ := hit["url"].(string)
		title, _ := hit["title"].(string)
		rec.touchURLTitle(url, title)
	}
}

// populateFetchTitle records the fetched page's title against the URL asked
// for and the URL the tool reports, which differ after a redirect.
func populateFetchTitle(rec *Record, requested, content string) {
	obj, ok := jsonToolPayload(content)
	if !ok {
		return
	}
	title, _ := obj["title"].(string)
	if strings.TrimSpace(title) == "" {
		return
	}
	rec.touchURLTitle(requested, title)
	if final, _ := obj["url"].(string); strings.TrimSpace(final) != "" {
		rec.touchURLTitle(final, title)
	}
}

func populateURLFromPayload(rec *Record, content string) {
	if rec == nil {
		return
	}
	obj, ok := jsonToolPayload(content)
	if !ok {
		return
	}
	if finalURL, ok := obj["final_url"].(string); ok && strings.TrimSpace(finalURL) != "" {
		rec.touchURL(finalURL)
	}
	if u, ok := obj["url"].(string); ok && strings.TrimSpace(u) != "" {
		rec.touchURL(u)
	}
}

func urlsInToolBody(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var out []string
	seen := map[string]struct{}{}
	for _, match := range toolBodyURLRE.FindAllString(text, -1) {
		url := strings.TrimSpace(match)
		if url == "" {
			continue
		}
		if _, dup := seen[url]; dup {
			continue
		}
		seen[url] = struct{}{}
		out = append(out, url)
	}
	return out
}

func populateReadRecord(projectDir string, rec *Record, args map[string]any, content string) {
	path := stringArg(args, "path")
	addRecordPath(projectDir, rec, path, FidelityStructured)
	if _, ok := NormalizeCitationPath(projectDir, path); !ok && strings.TrimSpace(projectDir) != "" {
		return
	}
	if payload, ok := parseReadPayload(content); ok {
		if len(payload.Ranges) > 0 {
			for _, block := range payload.Ranges {
				start := block.Offset
				end := block.EndLine
				if start <= 0 {
					start = 1
				}
				if end < start {
					end = start + CountTextLines(block.Content) - 1
					if end < start {
						end = start
					}
				}
				appendLineRange(rec, start, end)
				if body := strings.TrimSpace(block.Content); body != "" {
					rec.Body = append(rec.Body, body)
				}
			}
			return
		}
		// Outlines and empty symbol reads return no file text, so they cover no lines.
		if payload.Mode == "outline" || (payload.Mode == "symbol" && strings.TrimSpace(payload.Content) == "") {
			return
		}
		start := payload.Offset
		if payload.StartLine > 0 {
			start = payload.StartLine
		}
		end := payload.EndLine
		if start <= 0 {
			start = 1
		}
		if end < start {
			end = start + CountTextLines(payload.Content) - 1
			if end < start {
				end = start
			}
		}
		appendLineRange(rec, start, end)
		if body := strings.TrimSpace(payload.Content); body != "" {
			rec.Body = append(rec.Body, body)
		}
		return
	}
	body := stripToolHostSuffix(content)
	offset := intArg(args, "offset", 1)
	if offset <= 0 {
		offset = 1
	}
	limit := intArg(args, "limit", 0)
	lines := CountTextLines(body)
	end := offset + lines - 1
	if lines == 0 {
		end = offset
	}
	if limit > 0 && lines > limit {
		end = offset + limit - 1
	}
	appendLineRange(rec, offset, end)
	if strings.TrimSpace(body) != "" {
		rec.Body = append(rec.Body, body)
	}
}

// populateSummarizeRecord indexes the summarize response's anchors (path/line/
// excerpt) so a citation of a summarize#N span resolves verbatim.
func populateSummarizeRecord(projectDir string, rec *Record, content string) {
	for _, h := range summarizeAnchorDetails(content) {
		addRecordPath(projectDir, rec, h.path, FidelityStructured)
		rel, ok := NormalizeCitationPath(projectDir, h.path)
		if !ok || h.line <= 0 {
			continue
		}
		appendLineRange(rec, h.line, h.line)
		rec.addGrepLine(rel, h.line, h.content)
	}
}

func summarizeAnchorDetails(content string) []grepMatchDetail {
	obj, ok := jsonToolPayload(content)
	if !ok {
		return nil
	}
	raw, ok := obj["anchors"].([]any)
	if !ok {
		return nil
	}
	var out []grepMatchDetail
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		path, _ := m["path"].(string)
		line := jsonvalue.Int(m["line"])
		excerpt, _ := m["excerpt"].(string)
		path = NormalizeLedgerPath(path)
		if path == "" || line <= 0 {
			continue
		}
		out = append(out, grepMatchDetail{path: path, line: line, content: excerpt})
	}
	return out
}

func appendLineRange(rec *Record, start, end int) {
	if rec == nil || start <= 0 || end <= 0 || end < start {
		return
	}
	rec.LineRanges = append(rec.LineRanges, LineRange{Start: start, End: end})
}

func populateGrepRecord(projectDir string, rec *Record, args map[string]any, content string) {
	addRecordPath(projectDir, rec, stringArg(args, "path"), FidelityStructured)
	for _, p := range grepMatchPaths(content) {
		addRecordPath(projectDir, rec, p, FidelityStructured)
	}
	for _, m := range grepMatchDetails(content) {
		addRecordPath(projectDir, rec, m.path, FidelityStructured)
		rel, ok := NormalizeCitationPath(projectDir, m.path)
		if !ok || m.line <= 0 {
			continue
		}
		appendLineRange(rec, m.line, m.line)
		rec.addGrepLine(rel, m.line, m.content)
	}
	for _, h := range highlightDetails(content) {
		addRecordPath(projectDir, rec, h.path, FidelityStructured)
		rel, ok := NormalizeCitationPath(projectDir, h.path)
		if !ok || h.line <= 0 {
			continue
		}
		appendLineRange(rec, h.line, h.line)
		rec.addGrepLine(rel, h.line, h.content)
	}
}

func stripToolHostSuffix(content string) string {
	content = strings.TrimSpace(content)
	if idx := strings.Index(content, "\n>>>"); idx >= 0 {
		content = strings.TrimSpace(content[:idx])
	}
	return content
}

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

type grepMatchDetail struct {
	path    string
	line    int
	content string
}

type readToolPayload struct {
	Path    string `json:"path"`
	Mode    string `json:"mode"`
	Content string `json:"content"`
	Offset  int    `json:"offset"`
	// Symbol reads provide an explicit line span.
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
	Limit     int `json:"limit"`
	Ranges    []struct {
		Offset  int    `json:"offset"`
		EndLine int    `json:"end_line"`
		Content string `json:"content"`
	} `json:"ranges"`
}

func parseReadPayload(content string) (readToolPayload, bool) {
	obj, ok := jsonToolPayload(content)
	if !ok {
		return readToolPayload{}, false
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return readToolPayload{}, false
	}
	var payload readToolPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return readToolPayload{}, false
	}
	if strings.TrimSpace(payload.Path) == "" && strings.TrimSpace(payload.Content) == "" {
		return readToolPayload{}, false
	}
	return payload, true
}

func grepMatchDetails(content string) []grepMatchDetail {
	obj, ok := jsonToolPayload(content)
	if !ok {
		return nil
	}
	raw, ok := obj["matches"].([]any)
	if !ok {
		return nil
	}
	var out []grepMatchDetail
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		path, _ := m["path"].(string)
		line := jsonvalue.Int(m["line"])
		matchContent, _ := m["content"].(string)
		path = NormalizeLedgerPath(path)
		if path == "" || line <= 0 {
			continue
		}
		out = append(out, grepMatchDetail{
			path:    path,
			line:    line,
			content: matchContent,
		})
	}
	return out
}

func highlightDetails(content string) []grepMatchDetail {
	obj, ok := jsonToolPayload(content)
	if !ok {
		return nil
	}
	raw, ok := obj["highlights"].([]any)
	if !ok {
		return nil
	}
	var out []grepMatchDetail
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		path, _ := m["path"].(string)
		line := jsonvalue.Int(m["line"])
		matchContent, _ := m["content"].(string)
		path = NormalizeLedgerPath(path)
		if path == "" || line <= 0 {
			continue
		}
		out = append(out, grepMatchDetail{
			path:    path,
			line:    line,
			content: matchContent,
		})
	}
	return out
}

func intArg(args map[string]any, key string, defaultVal int) int {
	if args == nil {
		return defaultVal
	}
	switch v := args[key].(type) {
	case int:
		if v > 0 {
			return v
		}
	case int64:
		if v > 0 {
			return int(v)
		}
	case float64:
		if v > 0 {
			return int(v)
		}
	}
	return defaultVal
}

func CountTextLines(text string) int {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func jsonToolPayload(content string) (map[string]any, bool) {
	_, payload, _, ok := hostmarker.SplitToolJSONBody(content)
	if !ok {
		return nil, false
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return nil, false
	}
	return obj, true
}

func grepMatchPaths(content string) []string {
	obj, ok := jsonToolPayload(content)
	if !ok {
		return nil
	}
	raw, ok := obj["matches"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if p, ok := m["path"].(string); ok && strings.TrimSpace(p) != "" {
			out = append(out, NormalizeLedgerPath(p))
		}
	}
	return out
}

func findResultPaths(content string) []string {
	obj, ok := jsonToolPayload(content)
	if !ok {
		return nil
	}
	raw, ok := obj["results"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if p, ok := m["path"].(string); ok && strings.TrimSpace(p) != "" {
			out = append(out, NormalizeLedgerPath(p))
		}
	}
	return out
}

func listDirPaths(content string) []string {
	obj, ok := jsonToolPayload(content)
	if !ok {
		return nil
	}
	if p, ok := obj["path"].(string); ok && strings.TrimSpace(p) != "" {
		out := []string{NormalizeLedgerPath(p)}
		raw, ok := obj["entries"].([]any)
		if !ok {
			out = append(out, listDirMapTagFiles(obj)...)
			return out
		}
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if ep, ok := m["path"].(string); ok && strings.TrimSpace(ep) != "" {
				out = append(out, NormalizeLedgerPath(ep))
			}
		}
		return out
	}
	return nil
}

func listDirMapTagFiles(obj map[string]any) []string {
	raw, ok := obj["tags"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if f, ok := m["file"].(string); ok && strings.TrimSpace(f) != "" {
			out = append(out, NormalizeLedgerPath(f))
		}
	}
	return out
}

// HandlesSorted returns handle keys in tool-call assignment order.
func HandlesSorted(ev Ledger) []string {
	if len(ev.handleOrder) > 0 {
		out := make([]string, len(ev.handleOrder))
		copy(out, ev.handleOrder)
		return out
	}
	if len(ev.Handles) == 0 {
		return nil
	}
	out := make([]string, 0, len(ev.Handles))
	for h := range ev.Handles {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		ki, oi := ParseHandleOrdinal(out[i])
		kj, oj := ParseHandleOrdinal(out[j])
		if ki != kj {
			return ki < kj
		}
		return oi < oj
	})
	return out
}

func ParseHandleOrdinal(handle string) (kind string, ordinal int) {
	if !HandleGrammar.MatchString(handle) {
		return handle, 0
	}
	kind, digits, _ := strings.Cut(terminalHandle(handle), "#")
	ordinal, _ = strconv.Atoi(digits)
	return kind, ordinal
}

// declaredPathsFromArgs reads the path / paths[] pair multi-target filesystem
// tools declare.
func declaredPathsFromArgs(args map[string]any) []string {
	if p := strings.TrimSpace(stringArg(args, "path")); p != "" {
		return []string{p}
	}
	raw, ok := args["paths"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, it := range raw {
		if s, ok := it.(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func deleteResultPaths(content string) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	var resp struct {
		Deleted []string `json:"deleted"`
	}
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		return nil
	}
	return resp.Deleted
}
