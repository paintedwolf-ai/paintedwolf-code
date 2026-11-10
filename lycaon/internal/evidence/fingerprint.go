package evidence

import (
	"encoding/json"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/egress"
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
			rec.Custody.External = true
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
	case "command_output", "command_stop", "task", "delegate_dispatch", "render_view", "verify", "preview_overlay", "promote_overlay", "reject_overlay", "secret_generate", "secret_list", "secret_revoke":
		// Result receipts do not establish observations of referenced file contents.
		rec.Fidelity = FidelityStructured
		captureRecordBody(&rec, content)
	case "capture_page", "page_snapshot", "page_open":
		rec.Fidelity = FidelityStructured
		rec.Surface = SurfaceDOM
		rec.touchURL(stringArg(args, "url"))
		populateURLFromPayload(&rec, content)
		captureRecordBody(&rec, content)
	case "measure_page":
		rec.Fidelity = FidelityStructured
		rec.touchURL(stringArg(args, "url"))
		populateURLFromPayload(&rec, content)
		captureRecordBody(&rec, content)
	case "http_request":
		populateHTTPRequestRecord(projectDir, &rec, args, content)
	case "terminal_snapshot", "terminal_open", "terminal_send", "terminal_read", "terminal_close":
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

func populateHTTPRequestRecord(projectDir string, rec *Record, args map[string]any, content string) {
	if rec == nil {
		return
	}
	rec.Fidelity = FidelityStructured
	rec.Surface = SurfaceHTTP
	obj, hasJSON := jsonToolPayload(content)
	unixSocket := stringArg(args, "unix_socket")
	if unixSocket == "" && hasJSON {
		if s, ok := obj["unix_socket"].(string); ok {
			unixSocket = strings.TrimSpace(s)
		}
	}
	rawResolve := any(nil)
	if args != nil {
		rawResolve = args["resolve"]
	}
	var candidateURLs []string
	if reqURL := stringArg(args, "url"); reqURL != "" {
		candidateURLs = append(candidateURLs, reqURL)
	}
	if hasJSON {
		if redirects, ok := obj["redirects"].([]any); ok {
			for _, item := range redirects {
				if m, ok := item.(map[string]any); ok {
					if u, ok := m["url"].(string); ok && strings.TrimSpace(u) != "" {
						candidateURLs = append(candidateURLs, strings.TrimSpace(u))
					}
				}
			}
		}
		if finalURL, ok := obj["final_url"].(string); ok && strings.TrimSpace(finalURL) != "" {
			candidateURLs = append(candidateURLs, strings.TrimSpace(finalURL))
		}
		if u, ok := obj["url"].(string); ok && strings.TrimSpace(u) != "" {
			candidateURLs = append(candidateURLs, strings.TrimSpace(u))
		}
	}
	external := false
	for _, rawURL := range candidateURLs {
		if unixSocket != "" {
			if isSyntacticLoopbackURL(rawURL) {
				rec.touchURL(rawURL)
			}
			continue
		}
		rec.touchURL(rawURL)
		local, mappedExternalURL := isLocalHTTPURLWithResolve(rawURL, rawResolve)
		if !local {
			external = true
		}
		if mappedExternalURL != "" {
			rec.touchURL(mappedExternalURL)
		}
	}
	_, hasStatus := obj["status"]
	if hasJSON && hasStatus && unixSocket == "" && external {
		responsePath := stringArg(args, "response_path")
		if responsePath == "" {
			if p, ok := obj["response_path"].(string); ok {
				responsePath = strings.TrimSpace(p)
			}
		}
		responseBodyMode := strings.ToLower(stringArg(args, "response_body"))
		bodyUnavailable, _ := obj["body_unavailable"].(bool)
		if responsePath != "" {
			if written, ok := obj["written"].(bool); !ok || written {
				addRecordPath(projectDir, rec, responsePath, FidelityStructured)
				rec.Custody.External = true
			}
		} else if responseBodyMode != "discard" && !bodyUnavailable {
			rec.UntrustedContent = true
			if spillPath, ok := obj["body_spill_path"].(string); ok && strings.TrimSpace(spillPath) != "" {
				addRecordPath(projectDir, rec, strings.TrimSpace(spillPath), FidelityStructured)
			}
		}
	}
	captureRecordBody(rec, content)
}

func isSyntacticLoopbackURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Hostname() == "" {
		return false
	}
	return egress.SyntacticLoopback(u.Hostname())
}

func isLocalHTTPURLWithResolve(rawURL string, rawResolve any) (bool, string) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Hostname() == "" {
		return true, ""
	}
	host := u.Hostname()
	if !egress.SyntacticLoopback(host) {
		return false, ""
	}
	items, ok := rawResolve.([]any)
	if !ok || len(items) == 0 {
		return true, ""
	}
	reqPort := defaultURLPort(u)
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		mHost, _ := m["host"].(string)
		if !strings.EqualFold(strings.TrimSpace(mHost), host) {
			continue
		}
		mPort := intArg(m, "port", 0)
		if mPort > 0 && reqPort > 0 && mPort != reqPort {
			continue
		}
		ipStr, _ := m["ip"].(string)
		addr, err := netip.ParseAddr(strings.TrimSpace(ipStr))
		if err != nil {
			continue
		}
		addr = addr.Unmap()
		if !addr.IsLoopback() {
			mapped := *u
			if reqPort > 0 {
				mapped.Host = net.JoinHostPort(addr.String(), strconv.Itoa(reqPort))
			} else {
				mapped.Host = addr.String()
			}
			return false, mapped.String()
		}
	}
	return true, ""
}

func defaultURLPort(u *url.URL) int {
	if u == nil {
		return 0
	}
	if p := u.Port(); p != "" {
		n, _ := strconv.Atoi(p)
		return n
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return 80
	case "https":
		return 443
	default:
		return 0
	}
}

