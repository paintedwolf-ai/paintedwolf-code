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
	// Command surface occurrence values.
	"command_external_shown":       true,
	"platform_os":                  true,
	"session_scratch_dir":          true,
	"command_output_scratch_files": true,
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
