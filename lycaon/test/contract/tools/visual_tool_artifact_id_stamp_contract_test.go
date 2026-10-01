package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// visualToolResultAttachAllowlist limits production call sites of
// visual.AttachToolResult to ensure artifact_id stamping.
var visualToolResultAttachAllowlist = map[string]bool{
	"lycaon/internal/coordinator/promptloop/tools.go": true,
}

// TestVisualToolResultArtifactIDStampContract verifies artifact_id stamping
// on production visual captures.
func TestVisualToolResultArtifactIDStampContract(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")

	scan := scanVisualProduction(t, lycaonRoot)
	producers := scan.producers
	if len(producers) == 0 {
		t.Fatal("expected at least one production VisualCapture producer (Out.Visual = …)")
	}
	// Known floor — if a producer is removed, update this list; new ones are free.
	wantFloor := []string{
		"internal/tools/native/page/measure.go",
		"internal/tools/native/page/session.go",
		"internal/tools/native/page/render_view.go",
		"internal/tools/native/terminal/session_snapshot.go",
		"internal/visual/test_producer.go",
	}
	for _, rel := range wantFloor {
		if !producers[rel] {
			t.Errorf("missing expected producer %s — inventory drifted", rel)
		}
	}

	sidecarPath := filepath.Join(lycaonRoot, "internal", "visual", "sidecar.go")
	if !attachToolResultCallsStampArtifactID(t, sidecarPath) {
		t.Fatal("AttachToolResult must call StampArtifactID so Put+stamp stays atomic")
	}

	attachSites := scan.attachSites
	if len(attachSites) == 0 {
		t.Fatal("expected production AttachToolResult call site(s)")
	}
	var unexpected []string
	for site, usesReturn := range attachSites {
		if !visualToolResultAttachAllowlist[site] {
			unexpected = append(unexpected, site+" (not on allowlist — route tool visuals through AttachToolResult in promptloop only)")
			continue
		}
		if !usesReturn {
			unexpected = append(unexpected, site+" (discards stamped content — assign AttachToolResult's returned string to tool content)")
		}
	}
	sort.Strings(unexpected)
	for _, u := range unexpected {
		t.Error(u)
	}
	for allowed := range visualToolResultAttachAllowlist {
		if _, ok := attachSites[allowed]; !ok {
			t.Errorf("allowlist entry %s has no AttachToolResult call — update allowlist", allowed)
		}
	}

	bypass := scan.bypass
	for _, path := range bypass {
		t.Errorf("%s: VisualArtifact{ToolCallID:…} outside internal/visual/ bypasses AttachToolResult stamp — use AttachToolResult for tool-result Puts", path)
	}
}

type visualProductionScan struct {
	producers   map[string]bool
	attachSites map[string]bool
	bypass      []string
}

func scanVisualProduction(t *testing.T, lycaonRoot string) visualProductionScan {
	t.Helper()
	out := visualProductionScan{producers: map[string]bool{}, attachSites: map[string]bool{}}
	repoRoot := filepath.Dir(lycaonRoot)
	err := filepath.WalkDir(filepath.Join(lycaonRoot, "internal"), func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return walkErr
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		lycaonRel, err := filepath.Rel(lycaonRoot, path)
		if err != nil {
			lycaonRel = path
		}
		lycaonRel = filepath.ToSlash(lycaonRel)
		repoRel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			repoRel = path
		}
		repoRel = filepath.ToSlash(repoRel)
		bypasses := false
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.AssignStmt:
				for i, lhs := range node.Lhs {
					if i < len(node.Rhs) && isOutVisualSelector(lhs) && assignsVisualCapture(node.Rhs[i]) {
						out.producers[lycaonRel] = true
					}
				}
				for _, rhs := range node.Rhs {
					if !isAttachToolResultCall(rhs) {
						continue
					}
					usesReturn := len(node.Lhs) > 0
					if usesReturn {
						if id, ok := node.Lhs[0].(*ast.Ident); ok && id.Name == "_" {
							usesReturn = false
						}
					}
					if previous, exists := out.attachSites[repoRel]; exists {
						out.attachSites[repoRel] = previous && usesReturn
					} else {
						out.attachSites[repoRel] = usesReturn
					}
				}
			case *ast.ExprStmt:
				if isAttachToolResultCall(node.X) {
					out.attachSites[repoRel] = false
				}
			case *ast.CompositeLit:
				if strings.HasPrefix(lycaonRel, "internal/visual/") || !isVisualArtifactType(node.Type) {
					return true
				}
				for _, element := range node.Elts {
					field, ok := element.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, keyOK := field.Key.(*ast.Ident)
					if keyOK && key.Name == "ToolCallID" {
						bypasses = true
						break
					}
				}
			}
			return true
		})
		if bypasses {
			out.bypass = append(out.bypass, lycaonRel)
		}
		return nil
	})
	contractcheck.FailErr(t, "scan visual production AST", err)
	sort.Strings(out.bypass)
	return out
}

// attachToolResultCallsStampArtifactID AST-asserts AttachToolResult's body invokes StampArtifactID.
func attachToolResultCallsStampArtifactID(t *testing.T, sidecarPath string) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, sidecarPath, nil, 0)
	contractcheck.FailErr(t, "parse sidecar.go", err)
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != "AttachToolResult" || fn.Body == nil {
			continue
		}
		found := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				if fun.Name == "StampArtifactID" {
					found = true
					return false
				}
			case *ast.SelectorExpr:
				if fun.Sel != nil && fun.Sel.Name == "StampArtifactID" {
					found = true
					return false
				}
			}
			return true
		})
		return found
	}
	t.Fatal("AttachToolResult missing in sidecar.go")
	return false
}

func isOutVisualSelector(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Visual" {
		return false
	}
	// tctx.Out.Visual or Out.Visual
	switch x := sel.X.(type) {
	case *ast.Ident:
		return x.Name == "Out"
	case *ast.SelectorExpr:
		return x.Sel != nil && x.Sel.Name == "Out"
	default:
		return false
	}
}

func assignsVisualCapture(expr ast.Expr) bool {
	expr = stripUnary(expr)
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return false
	}
	switch typ := lit.Type.(type) {
	case *ast.Ident:
		return typ.Name == "VisualCapture"
	case *ast.SelectorExpr:
		return typ.Sel != nil && typ.Sel.Name == "VisualCapture"
	default:
		return false
	}
}

func stripUnary(expr ast.Expr) ast.Expr {
	for {
		u, ok := expr.(*ast.UnaryExpr)
		if !ok {
			return expr
		}
		expr = u.X
	}
}

func isAttachToolResultCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fun.Sel != nil && fun.Sel.Name == "AttachToolResult"
	case *ast.Ident:
		return fun.Name == "AttachToolResult"
	default:
		return false
	}
}

func isVisualArtifactType(expr ast.Expr) bool {
	switch typ := expr.(type) {
	case *ast.Ident:
		return typ.Name == "VisualArtifact"
	case *ast.SelectorExpr:
		return typ.Sel != nil && typ.Sel.Name == "VisualArtifact"
	default:
		return false
	}
}
