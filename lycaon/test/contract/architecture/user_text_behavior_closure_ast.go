package contract

import (
	"go/ast"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type userTextBehaviorScan struct {
	NLProbeToSinkViolations []string
	ErrTextParseViolations  []string
}

var userTextParamNames = map[string]bool{
	"userPrompt":  true,
	"userMessage": true,
	"message":     true,
	"text":        true,
}

var behaviorSinkCalls = map[string]bool{
	"Emit": true,
}

// These functions use text only for documented structural paths.
var nlProbeSinkExemptFuncs = map[string]map[string]bool{
	"internal/workflow/user_interaction.go": {
		"TryResolveUserFeedback": true,
		"ResolveUserFeedback":    true,
		"ResolveUserDecision":    true,
	},
	"internal/session/coordinator_batch.go": {
		"isVisibleUserIntentPrompt": true,
	},
	"internal/session/edit_followup.go": {
		"maybeQueueEditFollowUpRepeatKick": true,
		"editFollowUpRepeat":               true,
	},
	"internal/coordinator/surface/dispatch_hints.go": {
		"SessionUserTask": true,
	},
}

var nlStringProbeFuncs = map[string]bool{
	"Contains":    true,
	"HasPrefix":   true,
	"HasSuffix":   true,
	"EqualFold":   true,
	"MatchString": true,
}

var productionScanRoots = []string{
	"internal/session",
	"internal/coordinator",
	"internal/workflow",
	"internal/hitl",
	"internal/llm/providers",
	"internal/mcp",
	"internal/toolexecution",
	"internal/toolhost",
}

type userTextBehaviorCacheEntry struct {
	once sync.Once
	scan *userTextBehaviorScan
	err  error
}

var userTextBehaviorCache sync.Map // abs lycaon root → *userTextBehaviorCacheEntry

func scanUserTextBehaviorClosure(lycaonRoot string) (*userTextBehaviorScan, error) {
	abs, err := filepath.Abs(lycaonRoot)
	if err != nil {
		return nil, err
	}
	raw, _ := userTextBehaviorCache.LoadOrStore(abs, &userTextBehaviorCacheEntry{})
	entry := raw.(*userTextBehaviorCacheEntry)
	entry.once.Do(func() {
		entry.scan, entry.err = buildUserTextBehaviorClosure(abs)
	})
	return entry.scan, entry.err
}

func buildUserTextBehaviorClosure(lycaonRoot string) (*userTextBehaviorScan, error) {
	corp, err := contractcheck.LoadGoASTCorpus(lycaonRoot)
	if err != nil {
		return nil, err
	}
	out := &userTextBehaviorScan{}
	for _, gf := range corp.Files() {
		if gf.IsTest {
			continue
		}
		allowed := false
		for _, relRoot := range productionScanRoots {
			if strings.HasPrefix(gf.Rel, relRoot+"/") || gf.Rel == relRoot {
				allowed = true
				break
			}
		}
		if !allowed {
			continue
		}
		rel := gf.Rel
		fset := corp.Fset
		for _, decl := range gf.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			funcName := fn.Name.Name
			params := funcUserTextParams(fn)
			if nlProbeSinkExemptFuncs[rel] != nil && nlProbeSinkExemptFuncs[rel][funcName] {
				continue
			}
			hasProbe := funcBodyHasUserTextNLProbe(fn.Body, params)
			hasSink := funcBodyHasBehaviorSink(fn.Body)
			if hasProbe && hasSink {
				pos := fset.Position(fn.Pos())
				out.NLProbeToSinkViolations = append(out.NLProbeToSinkViolations,
					rel+":"+strconv.Itoa(pos.Line)+" "+funcName+" — user-text string probe reaches behavior sink")
			}
		}
		ast.Inspect(gf.AST, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			pos := fset.Position(call.Pos())
			site := rel + ":" + strconv.Itoa(pos.Line)
			recordErrTextParse(out, call, site)
			return true
		})
	}
	sort.Strings(out.NLProbeToSinkViolations)
	sort.Strings(out.ErrTextParseViolations)
	return out, nil
}

func funcUserTextParams(fn *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	if fn.Type.Params == nil {
		return out
	}
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if userTextParamNames[name.Name] {
				out[name.Name] = true
			}
		}
	}
	return out
}

func funcBodyHasUserTextNLProbe(body *ast.BlockStmt, params map[string]bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isNLStringProbeCall(call, params) {
			return true
		}
		found = true
		return false
	})
	return found
}

func funcBodyHasBehaviorSink(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if behaviorSinkCalls[contractcheck.CallFuncName(call.Fun)] {
			found = true
			return false
		}
		return true
	})
	return found
}

func isNLStringProbeCall(call *ast.CallExpr, params map[string]bool) bool {
	name := contractcheck.CallFuncName(call.Fun)
	if !nlStringProbeFuncs[name] {
		return false
	}
	if len(call.Args) == 0 {
		return false
	}
	if !isUserTextExpr(call.Args[0], params) {
		return false
	}
	if name == "Contains" && len(call.Args) >= 2 {
		if lit := contractcheck.AstStringLit(call.Args[1]); lit == "\n" {
			return false
		}
	}
	return true
}

func isUserTextExpr(expr ast.Expr, params map[string]bool) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return params[e.Name] || userTextParamNames[e.Name]
	case *ast.CallExpr:
		switch contractcheck.CallFuncName(e.Fun) {
		case "SessionUserTask":
			return true
		case "TrimSpace", "ToLower", "ToUpper":
			if len(e.Args) > 0 {
				return isUserTextExpr(e.Args[0], params)
			}
		}
	case *ast.SelectorExpr:
		if ident, ok := e.X.(*ast.Ident); ok {
			return params[ident.Name] || userTextParamNames[ident.Name]
		}
	}
	return false
}

func recordErrTextParse(out *userTextBehaviorScan, call *ast.CallExpr, site string) {
	name := contractcheck.CallFuncName(call.Fun)
	if name != "Contains" && name != "HasPrefix" && name != "HasSuffix" && name != "MatchString" {
		return
	}
	if len(call.Args) == 0 || !isErrErrorSelector(call.Args[0]) {
		return
	}
	out.ErrTextParseViolations = append(out.ErrTextParseViolations, site+" — strings."+name+"(err.Error(), …) forbidden; use typed sentinels + errors.Is")
}

func isErrErrorSelector(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Error" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "err"
}

var coordinatorPongoBooleanGateRE = regexp.MustCompile(`\{%\s*(?:if|elif)\s+(?:not\s+)?([a-z][a-z0-9_]*)\s*(?:%\}|(?:\s+(?:and|or)\s+))`)

// The nl branch is token-anchored so it matches the `nl_` prefix and not the
// letters inside ordinary words (inline, only, channel).
var bannedCoordinatorGateNameRE = regexp.MustCompile(`(?i)(greenfield|dispatch[_-]?hint|user[_-]?intent|(^|[_-])nl([_-]|$)|build[_-]?intent|chat[_-]?approv)`)

// coordinatorOccurrenceGates are values the coordinator render sites supply
// beside the reader facts.
var coordinatorOccurrenceGates = map[string]bool{
	// Round notice measurements come from the host's usage ledger for the round.
	"tokens":              true,
	"spend_under_cent":    true,
	"estimated_spend_usd": true,
	"latest_message_seq":  true,
	"round_ordinal":       true,
	"workflow_work_id":    true,
	"tools":               true,
	"tools_minutes":       true,
	// Prerequisite-blocked notice names the blocking producer worker when resolved.
	"producer": true,
	// Bounded grounding repair samples carry host-populated sample strings, truthy when non-empty.
	"observed_handles_sample": true,
	"observed_paths_sample":   true,
	"observed_urls_sample":    true,
	// Bounded grounding repair samples carry host-observed omitted counts.
	"observed_handles_omitted": true,
	"observed_paths_omitted":   true,
	"observed_urls_omitted":    true,
	"document_issues":          true,
	"agent_skills":             true,
	"rating_questions":         true,
	"agent_host_resources":     true,
	"has_file_tools":           true,
	"can_orient":               true,
	"can_spawn_web_research":   true,
	"can_spawn_workers":        true,
	// Kick counts of results awaiting landing and of partial workers.
	"pending_overlays": true,
	"partial_workers":  true,
	// The worker name a notice reports, empty for a round with no worker.
	"worker": true,
	// The settled round's notice, built from worker records and the job's report.
	"worker_result":          true,
	"leg_id":                 true,
	"progress_closure_armed": true,
	"card_label":             true,
	"card_rule":              true,
	"promoted_paths":         true,
	"drafted_synthesis":      true,
	// JSON projections of typed citation evaluation in guidance.CloseoutRepairHintData.
	"repair_observations":          true,
	"repair_targets":               true, // citation repair: rejected occurrences by repair ID
	"document_citation_targets":    true, // citation repair: rejected finding where entries
	"resume_work_allowed":          true,
	"can_recall":                   true, // recall in the closed turn's offered tools (promptloop rejectCloseoutGrounding)
	"offenders_sample":             true, // report-document repair: guidance.OffenderHintData
	"offenders_omitted":            true, // report-document repair: guidance.OffenderHintData
	"offending_keys":               true, // report-document repair: unread key names
	"allowed_top_level_keys":       true, // report-document repair: allowed top-level report fence keys
	"pinned_body_chars":            true, // report-document repair: char count of pinned body
	"retained_document":            true, // report-document repair: guidance.ReportDocumentFence
	"run_report":                   true, // report-document repair: the draft delivers its run's report
	"llm_timeout":                  true,
	"reason_text":                  true,
	"guidance":                     true,
	"options_criterion":            true,
	"topology_output":              true,
	"fanout_plan":                  true,
	"gate_obligations":             true,
	"completed_ago":                true,
	"worker_digest":                true,
	"primary_changed":              true,
	"changes":                      true, // projectroot.DiffRoots rows in RootsChangedKickData
	"active_root":                  true, // projectroot.ActiveRoot in RootsChangedKickData
	"session_folder_changed":       true, // session folder change in EnqueueRootsChangedKick
	"label":                        true, // detached root label in enqueueRootDetachCanceledKick
	"evidence_digest":              true,
	"command_completion":           true,
	"command_refusal":              true,
	"held_call_handle":             true, // host-supplied handle of a settled held call
	"scan_categories":              true,
	"scan_findings_count":          true,
	"scan_id":                      true,
	"scanner_id":                   true,
	"obligations":                  true,
	"last_worker_decision_request": true,
	"review_verdict":               true,
	"spawnable_reviewers":          true,
	"review_followup_attempts":     true,
	"command_external_changed":     true,
	"command_external_hidden":      true,
}

type coordinatorGateSite struct {
	rel  string
	gate string
}

type coordinatorGateScan struct {
	files int
	sites []coordinatorGateSite
}

func scanCoordinatorPongoBooleanGates(catalogRoot string) (coordinatorGateScan, error) {
	var out coordinatorGateScan
	err := filepath.WalkDir(catalogRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		base := filepath.Base(path)
		if !strings.HasPrefix(base, "coordinator") && !strings.Contains(path, string(filepath.Separator)+"partials"+string(filepath.Separator)+"coordinator") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(catalogRoot, path)
		if err != nil {
			return err
		}
		out.files++
		for _, m := range coordinatorPongoBooleanGateRE.FindAllStringSubmatch(string(raw), -1) {
			gate := m[1]
			if gate == "not" || gate == "forloop" {
				continue
			}
			out.sites = append(out.sites, coordinatorGateSite{rel: filepath.ToSlash(rel), gate: gate})
		}
		return nil
	})
	return out, err
}

var allowedKickTriggerClasses = map[string]bool{
	"manifest_on_reenter": true,
	"phase_on_enter":      true,
	"tool_rejection":      true,
	"host_api":            true,
	"wait_trigger":        true,
	"observed_history":    true,
	"workflow_event":      true,
	"overlay_state":       true,
}

var forbiddenKickFiresInSubstrings = []string{
	"greenfield", "user message contains", "strings.Contains", "phrase list",
	"chat yes", "lgtm", "nl intent",
}
