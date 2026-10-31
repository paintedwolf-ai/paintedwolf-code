package evidence

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/ingestion"
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
		captureRecordBody(&rec, content)
	case "jq_edit":
		target := stringArg(args, "dest")
		if target == "" {
			target = stringArg(args, "path")
		}
		addRecordPath(projectDir, &rec, target, FidelityStructured)
		captureRecordBody(&rec, content)
	case "code_rewrite":
		for _, p := range declaredPathsFromArgs(args) {
			addRecordPath(projectDir, &rec, p, FidelityStructured)
		}
		captureRecordBody(&rec, content)
	case "delete":
		for _, p := range declaredPathsFromArgs(args) {
			addRecordPath(projectDir, &rec, p, FidelityStructured)
		}
		for _, p := range deleteResultPaths(content) {
			addRecordPath(projectDir, &rec, p, FidelityStructured)
		}
		captureRecordBody(&rec, content)
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
	case "render_view", "verify", "preview_overlay", "promote_overlay", "reject_overlay":
		// Result receipts do not establish observations of referenced file contents.
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
