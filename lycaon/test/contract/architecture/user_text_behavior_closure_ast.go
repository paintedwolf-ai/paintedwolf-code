package contract

// AST scans enforce coordinator behavior channels.

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

	"github.com/lycaon/lycaon/pkg/testcorpus"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type userTextBehaviorScan struct {
	UnregisteredPromptKickHooks []string
	NLProbeToSinkViolations     []string
	ErrTextParseViolations      []string
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
}

// These hooks may run before the visible user turn.
var registeredPromptPathKickHooks = map[string]string{
	"maybeQueueEditFollowUpRepeatKick": "observed_history",
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
	if err := scanPromptPathKickHooks(corp, out); err != nil {
		return nil, err
	}
	sort.Strings(out.UnregisteredPromptKickHooks)
	sort.Strings(out.NLProbeToSinkViolations)
	sort.Strings(out.ErrTextParseViolations)
	return out, nil
}

func scanPromptPathKickHooks(corp *testcorpus.GoCorpus, out *userTextBehaviorScan) error {
	for _, gf := range corp.Files() {
		if gf.IsTest || !strings.HasPrefix(gf.Rel, "internal/session/") {
			continue
		}
		for _, decl := range gf.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue
			}
			funcName := fn.Name.Name
			if !strings.HasPrefix(funcName, "maybeQueue") || !strings.HasSuffix(funcName, "Kick") {
				continue
			}
			if _, ok := registeredPromptPathKickHooks[funcName]; !ok {
				pos := corp.Fset.Position(fn.Pos())
				out.UnregisteredPromptKickHooks = append(out.UnregisteredPromptKickHooks,
					gf.Rel+":"+strconv.Itoa(pos.Line)+" "+funcName)
			}
		}
	}
	return nil
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

var registeredCoordinatorBooleanGates = map[string]bool{
	"document_issues":                              true,
	"agent_skills":                                 true,
	"rating_questions":                             true,
	"agent_has_skill_ask_for_a_decision":           true,
	"agent_has_skill_research_current_information": true,
	"agent_has_skill_mock_before_build":            true,
	"agent_has_skill_verify_visual_change":         true,
	"agent_has_skill_verify_terminal_change":       true,
	"agent_host_resources":                         true,
	"surface_card":                                 true,
	"more_tools_loadable":                          true,
	"pending_overlay_promote":                      true,
	"verify_required":                              true,
	"verify_command":                               true,
	"has_file_tools":                               true,
	"can_orient":                                   true,
	"can_spawn_web_research":                       true,
	"can_spawn_workers":                            true,
	"web_search_enabled":                           true,
	"partial_worker_jobs":                          true,
	"pending_overlay_jobs":                         true,
	"leg_id":                                       true,
	"at_host_cap":                                  true,
	"finish_note":                                  true,
	"agent_tools":                                  true,
	"requestable_tools":                            true,
	"write_globs":                                  true,
	"reject_codes":                                 true,
	"has_find_tool":                                true,
	"prefer_native_file_tools":                     true,
	"host_runner_tools":                            true,
	"profile_has_verify":                           true,
	"profile_has_command":                          true,
	"profile_has_http_request":                     true,
	"http_request_needs_request":                   true,
	"managed_secrets_needs_request":                true,
	"profile_has_fetch_url":                        true,
	"fetch_url_needs_request":                      true,
	"profile_has_task":                             true,
	"profile_has_wait":                             true,
	"wait_needs_request":                           true,
	"profile_has_scan_drilldown":                   true,
	"profile_has_list_dir":                         true,
	"profile_has_write_tools":                      true,
	"profile_mutation_capable":                     true,
	"profile_has_read_tools":                       true,
	"profile_has_grep":                             true,
	"profile_has_code_rewrite":                     true,
	"profile_has_read":                             true,
	"profile_has_jq":                               true,
	"profile_has_survey_repo":                      true,
	"profile_has_summarize":                        true,
	"profile_has_ask_user":                         true,
	"profile_has_skills_read":                      true,
	"profile_has_surface_note":                     true,
	"profile_has_recall":                           true,
	"progress_closure_armed":                       true,
	"card_edit_deferred":                           true,
	"card_label":                                   true,
	"card_may_dispatch":                            true,
	"card_may_edit":                                true,
	"card_may_run":                                 true,
	"card_only_tools":                              true,
	"card_requestable":                             true,
	"card_rule":                                    true,
	"surface_card_label":                           true,
	"profile_has_terminal_open":                    true,
	"profile_has_terminal_snapshot":                true,
	"profile_has_render_view":                      true,
	"profile_has_capture_page":                     true,
	"profile_has_page_open":                        true,
	"profile_has_page_controls":                    true,
	"profile_has_page_snapshot":                    true,
	"profile_has_page_act":                         true,
	"profile_has_page_close":                       true,
	"visual_show_available":                        true,
	"visual_show_page":                             true,
	"visual_show_terminal":                         true,
	"visual_show_needs_request":                    true,
	// Capability-group roster fact (prompts.MergeInlineCapabilityVars).
	"inline_run_needs_request": true,
	"profile_git_tools":        true,
	"agent_type":               true,
	"job_id":                   true,
	"candidates":               true,
	"promoted_paths":           true,
	"drafted_synthesis":        true,
	// JSON projections of typed citation evaluation in guidance.CloseoutRepairHintData.
	"repair_observations":          true,
	"retained_citations":           true,
	"offenders_sample":             true, // report-document repair: guidance.OffenderHintData
	"offenders_omitted":            true, // report-document repair: guidance.OffenderHintData
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
	"active_root":                  true, // projectroot.ActiveRoot in RootsChangedKickData
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
	"worker_budget_exhausted":      true,
	"budget_request_open":          true,
	"at_host_max":                  true,
	"request_open":                 true,
	"review_verdict":               true,
	"spawnable_reviewers":          true,
	// Roster ids from spawn_roster_surface.go, gated on being non-empty.
	"spawn_read_agent_ids":  true,
	"spawn_write_agent_ids": true,
}

func scanCoordinatorPongoBooleanGates(catalogRoot string) ([]string, error) {
	var violations []string
	err := filepath.WalkDir(catalogRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
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
		rel = filepath.ToSlash(rel)
		text := string(raw)
		for _, m := range coordinatorPongoBooleanGateRE.FindAllStringSubmatch(text, -1) {
			if len(m) < 2 {
				continue
			}
			gate := m[1]
			if gate == "execution_mode" || gate == "not" || gate == "forloop" {
				continue
			}
			if bannedCoordinatorGateNameRE.MatchString(gate) {
				violations = append(violations, rel+": forbidden NL-style prompt gate "+gate)
				continue
			}
			if !registeredCoordinatorBooleanGates[gate] {
				violations = append(violations, rel+": unregistered coordinator boolean gate "+gate+" — add typed Go source or registeredCoordinatorBooleanGates")
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(violations)
	return violations, nil
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
	"greenfield",
	"user message contains",
	"strings.Contains",
	"phrase list",
	"chat yes",
	"lgtm",
	"nl intent",
}
