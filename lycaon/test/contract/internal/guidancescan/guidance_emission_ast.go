package guidancescan

// AST inventory for agent-feedback emission sites.

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/testcorpus"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var HintCodeShape = regexp.MustCompile(`^[A-Z][A-Z0-9_]+$`)

// guidanceEmissionScan is the live inventory of agent-feedback emission sites.
type guidanceEmissionScan struct {
	// FormatCalls maps hint code → file:line sites (.Format / FormatCoordinatorNudge).
	FormatCalls map[string][]string
	// HintCodeRefs maps registered hint code → file:line sites (string literals in production code).
	HintCodeRefs map[string][]string
	// QueuePendingTextSites lists production call sites of QueuePendingText.
	QueuePendingTextSites []string
	// GroundingNudgeMessageField lists ErrGroundingNudge literals that set Message (forbidden).
	GroundingNudgeMessageField []string
	// InlineCodeNudgeLiterals lists "Code: FOO — …" string literals in coordinator packages.
	InlineCodeNudgeLiterals []string
}

type guidanceEmissionCacheEntry struct {
	once sync.Once
	scan *guidanceEmissionScan
	err  error
}

var guidanceEmissionCache sync.Map // abs lycaon root → *guidanceEmissionCacheEntry

// ScanGuidanceEmission returns the cached emission inventory for lycaonRoot.
func ScanGuidanceEmission(lycaonRoot string) (*guidanceEmissionScan, error) {
	abs, err := filepath.Abs(lycaonRoot)
	if err != nil {
		return nil, err
	}
	raw, _ := guidanceEmissionCache.LoadOrStore(abs, &guidanceEmissionCacheEntry{})
	entry := raw.(*guidanceEmissionCacheEntry)
	entry.once.Do(func() {
		entry.scan, entry.err = buildGuidanceEmissionScan(abs)
	})
	return entry.scan, entry.err
}

func buildGuidanceEmissionScan(lycaonRoot string) (*guidanceEmissionScan, error) {
	cfg, err := guidance.LoadHintConfigStock()
	if err != nil {
		return nil, err
	}
	registered := make(map[string]bool, len(cfg.HintCodes))
	for code := range cfg.HintCodes {
		registered[code] = true
	}
	corp, err := contractcheck.LoadGoASTCorpus(lycaonRoot)
	if err != nil {
		return nil, err
	}
	out := &guidanceEmissionScan{
		FormatCalls:  make(map[string][]string),
		HintCodeRefs: make(map[string][]string),
	}
	for _, gf := range corp.Files() {
		rel := gf.Rel
		isTest := gf.IsTest
		fset := corp.Fset
		ast.Inspect(gf.AST, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				if !isTest {
					recordFormatCall(out, fset, rel, node)
				}
				recordQueuePendingText(out, fset, rel, isTest, node)
			case *ast.CompositeLit:
				recordGroundingNudgeMessage(out, fset, rel, isTest, node)
			case *ast.BasicLit:
				recordInlineCodeLiteral(out, fset, rel, isTest, node)
				recordHintCodeLiteralRef(out, fset, rel, isTest, registered, node)
			}
			return true
		})
	}
	return out, nil
}

func recordFormatCall(out *guidanceEmissionScan, fset *token.FileSet, rel string, call *ast.CallExpr) {
	code, ok := formatCallHintCode(call)
	if !ok {
		return
	}
	pos := fset.Position(call.Pos())
	site := rel + ":" + strconv.Itoa(pos.Line)
	out.FormatCalls[code] = guidanceAppendUnique(out.FormatCalls[code], site)
}

func formatCallHintCode(call *ast.CallExpr) (string, bool) {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		switch fn.Sel.Name {
		case "FormatCoordinatorNudge":
		case "Format":
			if !isRejectFormatterSelector(fn.X) {
				return "", false
			}
		default:
			return "", false
		}
	case *ast.Ident:
		if fn.Name != "FormatCoordinatorNudge" {
			return "", false
		}
	default:
		return "", false
	}
	if len(call.Args) == 0 {
		return "", false
	}
	code := contractcheck.AstStringLit(call.Args[0])
	if !HintCodeShape.MatchString(code) {
		return "", false
	}
	return code, true
}

func isRejectFormatterSelector(x ast.Expr) bool {
	switch e := x.(type) {
	case *ast.Ident:
		switch e.Name {
		case "f", "rejectFmt", "RejectFmt", "formatter", "RejectFormatter":
			return true
		}
	case *ast.SelectorExpr:
		switch e.Sel.Name {
		case "RejectFmt", "rejectFmt":
			return true
		}
	}
	return false
}

func recordQueuePendingText(out *guidanceEmissionScan, fset *token.FileSet, rel string, isTest bool, call *ast.CallExpr) {
	name := contractcheck.CallFuncName(call.Fun)
	if name != "QueuePendingText" {
		return
	}
	if isTest {
		return
	}
	pos := fset.Position(call.Pos())
	out.QueuePendingTextSites = append(out.QueuePendingTextSites, rel+":"+strconv.Itoa(pos.Line))
}

func recordGroundingNudgeMessage(out *guidanceEmissionScan, fset *token.FileSet, rel string, isTest bool, lit *ast.CompositeLit) {
	if isTest {
		return
	}
	typeName := contractcheck.CompositeLitTypeName(lit.Type)
	if typeName != "ErrGroundingNudge" {
		return
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Message" {
			continue
		}
		if contractcheck.AstStringLit(kv.Value) == "" {
			continue
		}
		pos := fset.Position(kv.Pos())
		out.GroundingNudgeMessageField = append(out.GroundingNudgeMessageField, rel+":"+strconv.Itoa(pos.Line))
	}
}

func recordInlineCodeLiteral(out *guidanceEmissionScan, fset *token.FileSet, rel string, isTest bool, lit *ast.BasicLit) {
	if isTest || lit.Kind != token.STRING {
		return
	}
	if !strings.HasPrefix(rel, "internal/session/") &&
		!strings.HasPrefix(rel, "internal/delegation/") &&
		!strings.HasPrefix(rel, "internal/coordinator/") {
		return
	}
	val := contractcheck.AstStringLit(lit)
	if !strings.Contains(val, "Code:") {
		return
	}
	// Ad-hoc one-line nudges: "Code: FOO — prose" or "Code: FOO - prose"
	if !strings.Contains(val, "—") && !strings.Contains(val, " - ") {
		return
	}
	pos := fset.Position(lit.Pos())
	out.InlineCodeNudgeLiterals = append(out.InlineCodeNudgeLiterals, rel+":"+strconv.Itoa(pos.Line))
}

func recordHintCodeLiteralRef(
	out *guidanceEmissionScan,
	fset *token.FileSet,
	rel string,
	isTest bool,
	registered map[string]bool,
	lit *ast.BasicLit,
) {
	if isTest || lit.Kind != token.STRING {
		return
	}
	if !strings.HasPrefix(rel, "internal/") {
		return
	}
	code := contractcheck.AstStringLit(lit)
	if !registered[code] {
		return
	}
	pos := fset.Position(lit.Pos())
	out.HintCodeRefs[code] = guidanceAppendUnique(out.HintCodeRefs[code], rel+":"+strconv.Itoa(pos.Line))
}

func (s *guidanceEmissionScan) HasEmissionSite(code string) bool {
	if len(s.FormatCalls[code]) > 0 || len(s.HintCodeRefs[code]) > 0 {
		return true
	}
	return false
}

// ScanInlineEnvelopeHintConsts finds `const fooHint = "long prose"` patterns in tool envelope packages.
func ScanInlineEnvelopeHintConsts(lycaonRoot string) ([]string, error) {
	corp, err := contractcheck.SourceLoader.Load(lycaonRoot, testcorpus.Options{Extensions: []string{".go"}})
	if err != nil {
		return nil, err
	}
	var sites []string
	for _, f := range corp.Files() {
		if strings.HasSuffix(f.Rel, "_test.go") {
			continue
		}
		if !strings.HasPrefix(f.Rel, "internal/scan/") && !strings.HasPrefix(f.Rel, "internal/repomap/") {
			continue
		}
		for _, line := range strings.Split(f.Text(), "\n") {
			trim := strings.TrimSpace(line)
			if !strings.HasPrefix(trim, "const ") || !strings.Contains(trim, "Hint = \"") {
				continue
			}
			if strings.Contains(trim, "HintCode") {
				continue
			}
			sites = append(sites, f.Rel+": "+trim)
		}
	}
	sort.Strings(sites)
	return sites, nil
}

func guidanceAppendUnique(slice []string, v string) []string {
	for _, s := range slice {
		if s == v {
			return slice
		}
	}
	return append(slice, v)
}

// CodesReferencedInAgentToolProfilesYAML returns hint codes wired via runtime_rules.code.
func CodesReferencedInAgentToolProfilesYAML(path string) (map[string]bool, error) {
	out := make(map[string]bool)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, m := range regexp.MustCompile(`\bcode:\s*([A-Z][A-Z0-9_]+)\b`).FindAllStringSubmatch(string(raw), -1) {
		if len(m) > 1 {
			out[m[1]] = true
		}
	}
	return out, nil
}

type rulesYAMLCodesCacheEntry struct {
	once  sync.Once
	codes map[string]bool
	err   error
}

var rulesYAMLCodesCache sync.Map // abs rules dir → *rulesYAMLCodesCacheEntry

var rulesYAMLCodeRE = regexp.MustCompile(`code:\s*([A-Z][A-Z0-9_]+)`)

func CodesReferencedInRulesYAML(rulesDir string) (map[string]bool, error) {
	abs, err := filepath.Abs(rulesDir)
	if err != nil {
		return nil, err
	}
	raw, _ := rulesYAMLCodesCache.LoadOrStore(abs, &rulesYAMLCodesCacheEntry{})
	entry := raw.(*rulesYAMLCodesCacheEntry)
	entry.once.Do(func() {
		entry.codes, entry.err = buildRulesYAMLCodes(abs)
	})
	return entry.codes, entry.err
}

func buildRulesYAMLCodes(rulesDir string) (map[string]bool, error) {
	corp, err := contractcheck.SourceLoader.Load(rulesDir, testcorpus.Options{Extensions: []string{".yaml"}})
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool)
	for _, f := range corp.Files() {
		for _, m := range rulesYAMLCodeRE.FindAllStringSubmatch(f.Text(), -1) {
			if len(m) > 1 {
				out[m[1]] = true
			}
		}
	}
	return out, nil
}
