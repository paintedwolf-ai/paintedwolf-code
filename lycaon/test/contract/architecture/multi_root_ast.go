package contract

import (
	"go/ast"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/pkg/testcorpus"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type multiRootASTScan struct {
	SingleRootJoins      []string
	ActiveRootPathJoins  []string
	DuplicateAddressing  []string
	PromptWithoutPartial []string
	HostPathTemplateVars []string
}

// multiRootJoinAllowlist applies only to scanned tool files.
var multiRootJoinAllowlist = map[string]string{
	"internal/tools/projectpaths/projectpaths.go": "resolver hub joins roots with validated relative paths",
}

var multiRootActiveRootJoinAllowlist = map[string]string{
	"internal/tools/registry.go": "ActiveRootPath helper only",
}

var multiRootAddressingSSOT = regexp.MustCompile(`@<label>|cwd="@<label>"|spans \{\{ root_count \}\} folders`)

type multiRootASTCacheEntry struct {
	once sync.Once
	scan *multiRootASTScan
	err  error
}

var multiRootASTCache sync.Map // abs lycaon root → *multiRootASTCacheEntry

func scanMultiRootAST(lycaonRoot string) (*multiRootASTScan, error) {
	abs, err := filepath.Abs(lycaonRoot)
	if err != nil {
		return nil, err
	}
	raw, _ := multiRootASTCache.LoadOrStore(abs, &multiRootASTCacheEntry{})
	entry := raw.(*multiRootASTCacheEntry)
	entry.once.Do(func() {
		entry.scan, entry.err = buildMultiRootAST(abs)
	})
	return entry.scan, entry.err
}

func buildMultiRootAST(lycaonRoot string) (*multiRootASTScan, error) {
	corp, err := contractcheck.LoadGoASTCorpus(lycaonRoot)
	if err != nil {
		return nil, err
	}
	mdText, err := contractcheck.SourceLoader.Load(lycaonRoot, testcorpus.Options{Extensions: []string{".md"}})
	if err != nil {
		return nil, err
	}
	out := &multiRootASTScan{}
	for _, gf := range corp.Files() {
		if gf.IsTest {
			continue
		}
		switch {
		case strings.HasPrefix(gf.Rel, "internal/tools/"):
			scanMultiRootToolFile(corp, gf, out)
		case strings.HasPrefix(gf.Rel, "internal/prompts/"):
			scanMultiRootPromptGoFile(gf, out)
		}
	}
	for _, f := range mdText.Files() {
		if strings.HasPrefix(f.Rel, "config/") {
			scanMultiRootTemplateData(f.Rel, f.Text(), out)
		}
	}
	sort.Strings(out.SingleRootJoins)
	sort.Strings(out.ActiveRootPathJoins)
	sort.Strings(out.DuplicateAddressing)
	sort.Strings(out.PromptWithoutPartial)
	sort.Strings(out.HostPathTemplateVars)
	return out, nil
}

func scanMultiRootToolFile(corp *testcorpus.GoCorpus, gf testcorpus.GoFile, out *multiRootASTScan) {
	ast.Inspect(gf.AST, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Join" {
			return true
		}
		if len(call.Args) < 2 {
			return true
		}
		base := exprString(call.Args[0])
		second := exprString(call.Args[1])
		pos := corp.Fset.Position(call.Pos())
		site := gf.Rel + ":" + strconv.Itoa(pos.Line)
		if strings.Contains(base, "projectDir") || strings.Contains(base, "ProjectDir") || base == "root" {
			if _, allowed := multiRootJoinAllowlist[gf.Rel]; !allowed && !gitRepositoryEntryJoin(gf, call) {
				out.SingleRootJoins = append(out.SingleRootJoins, site+" base="+base+" arg="+second)
			}
		}
		if strings.Contains(base, "ActiveRootPath") {
			if _, allowed := multiRootActiveRootJoinAllowlist[gf.Rel]; !allowed {
				out.ActiveRootPathJoins = append(out.ActiveRootPathJoins, site)
			}
		}
		return true
	})
}

// multiRootHostPathVar matches output expressions that render a host absolute
// path: the workspace-root, scratch, and worker-workspace vars, and member
// accesses on root-change data. Curator evidence rows keep c.path — a
// normalized display path, not a host path.
var multiRootHostPathVar = regexp.MustCompile(`\b(?:project_dir|session_scratch_dir|worker_workspace_path|(?:r|c|root|new_primary|active_root)\.path|c\.old_path)\b`)

// multiRootTemplateOutput extracts {{ ... }} output expressions. Statement
// tags ({% if %} guards) never render a value, so they are not host paths.
var multiRootTemplateOutput = regexp.MustCompile(`\{\{([^}]*)\}\}`)

// multiRootCoordinatorKick matches the coordinator kick guidance files, where
// a bare {{ path }} rendered the detached root's host path.
var multiRootCoordinatorKick = regexp.MustCompile(`config/packs/painted-wolf/platform/guidance/coordinator-[^/]*\.md`)

func scanMultiRootTemplateData(rel, text string, out *multiRootASTScan) {
	if multiRootAddressingSSOT.MatchString(text) {
		if rel != "config/packs/painted-wolf/platform/shared/partials/multi-root-addressing.md" &&
			rel != "config/packs/painted-wolf/platform/shared/partials/workspace-roots.md" &&
			rel != "config/packs/painted-wolf/platform/guidance/board-orientation.md" &&
			rel != "config/packs/painted-wolf/platform/guidance/coordinator-roots-changed.md" {
			out.DuplicateAddressing = append(out.DuplicateAddressing, rel)
		}
	}
	if strings.Contains(text, "Working directory:") && !strings.Contains(text, "workspace-roots.md") && !strings.Contains(text, `include "partials/workspace-roots.md"`) {
		if rel != "config/packs/painted-wolf/platform/shared/partials/workspace-roots.md" {
			out.PromptWithoutPartial = append(out.PromptWithoutPartial, rel+": Working directory:")
		}
	}
	for _, m := range multiRootTemplateOutput.FindAllStringSubmatch(text, -1) {
		expr := strings.TrimSpace(m[1])
		if hit := multiRootHostPathVar.FindString(expr); hit != "" {
			if hit == "c.path" && rel == "config/packs/painted-wolf/platform/guidance/curator/user.md" {
				continue
			}
			out.HostPathTemplateVars = append(out.HostPathTemplateVars, rel+": {{"+expr+"}}")
			continue
		}
		if expr == "path" && multiRootCoordinatorKick.MatchString(rel) {
			out.HostPathTemplateVars = append(out.HostPathTemplateVars, rel+": {{"+expr+"}}")
		}
	}
}

func scanMultiRootPromptGoFile(gf testcorpus.GoFile, out *multiRootASTScan) {
	body := gf.Text()
	if strings.Contains(body, "project_dir") && !strings.Contains(body, "MergeWorkspaceRootsVars") && !strings.Contains(body, "workspace-roots") {
		if gf.Rel != "internal/prompts/workspace_roots.go" {
			out.PromptWithoutPartial = append(out.PromptWithoutPartial, gf.Rel+": project_dir")
		}
	}
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.CallExpr:
		if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
			return exprString(sel.X) + "." + sel.Sel.Name + "()"
		}
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	}
	return ""
}

// Git index entries are relative to the repository resolved from the reached
// directory (-C included), not an assumed project root. Bound the exception to
// its single-return join; other functions and additional statements still scan.
func gitRepositoryEntryJoin(file testcorpus.GoFile, call *ast.CallExpr) bool {
	if file.Rel != "internal/tools/native/gitcmd/file_entries.go" {
		return false
	}
	for _, decl := range file.AST.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "entryAbs" || fn.Body == nil || len(fn.Body.List) != 1 {
			continue
		}
		ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
		if ok && len(ret.Results) == 1 && ret.Results[0] == call {
			return true
		}
	}
	return false
}
