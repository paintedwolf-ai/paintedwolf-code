package contract

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// platformAdapterPackages declares the packages under lycaon/internal that own
// platform differences. Each reports typed facts, with unsupported as a typed
// result, and is the only place the command environment's platform code lives.
func platformAdapterPackages() []string {
	return []string{"confine", "exec", "fseffect", "fspath", "hostos", "hostprocess", "lineage", "osprocess"}
}

// platformConsumerRoots are the command language, evaluator, native utilities
// and their composition. They consume adapter facts and never branch on the
// platform themselves.
func platformConsumerRoots() []string {
	return []string{"commandsurface", "commandprogram", "tools", "toolhost"}
}

var (
	platformOSNames = []string{
		"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos", "ios", "js", "linux",
		"netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows", "zos", "unix", "other",
	}
	platformArchNames = []string{
		"386", "amd64", "arm", "arm64", "loong64", "mips", "mips64", "mips64le", "mipsle",
		"ppc64", "ppc64le", "riscv64", "s390x", "wasm",
	}
)

func TestCommandEnvironmentPlatformCodeLivesInAdapters(t *testing.T) {
	t.Parallel()
	internal := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal")
	findings := platformFindings(t, internal, platformConsumerRoots())
	contractcheck.FailViolations(t, "platform differences outside the declared platform adapters ("+strings.Join(platformAdapterPackages(), ", ")+")", findings)
}

// The declaration stays exact: every declared adapter exists, carries platform
// code, and sits outside the consumers, so declaring a consumer can never
// satisfy the rule.
func TestPlatformAdapterDeclarationIsCurrent(t *testing.T) {
	t.Parallel()
	internal := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal")
	var problems []string
	for _, adapter := range platformAdapterPackages() {
		for _, consumer := range platformConsumerRoots() {
			if adapter == consumer || strings.HasPrefix(adapter, consumer+"/") {
				problems = append(problems, adapter+": declared adapter lies inside consumer root "+consumer)
			}
		}
		if info, err := os.Stat(filepath.Join(internal, filepath.FromSlash(adapter))); err != nil || !info.IsDir() {
			problems = append(problems, adapter+": declared adapter does not exist")
			continue
		}
		if len(platformFindings(t, internal, []string{adapter})) == 0 {
			problems = append(problems, adapter+": declared adapter carries no platform code")
		}
	}
	contractcheck.FailViolations(t, "platform adapter declaration drift", problems)
}

// platformFindings lists production files under roots that are platform
// constrained or check runtime.GOOS / runtime.GOARCH.
func platformFindings(t *testing.T, internal string, roots []string) []string {
	t.Helper()
	fset := token.NewFileSet()
	var findings []string
	for _, root := range roots {
		dir := filepath.Join(internal, filepath.FromSlash(root))
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(internal, path)
			rel = filepath.ToSlash(rel)
			if platformFileSuffix(d.Name()) {
				findings = append(findings, rel+": platform-suffixed file")
			}
			file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				return err
			}
			if platformBuildConstraint(file) {
				findings = append(findings, rel+": platform build constraint")
			}
			alias := contractcheck.ImportAliasFor(file, "runtime")
			if alias == "" {
				return nil
			}
			ast.Inspect(file, func(n ast.Node) bool {
				selector, ok := n.(*ast.SelectorExpr)
				if !ok || (selector.Sel.Name != "GOOS" && selector.Sel.Name != "GOARCH") {
					return true
				}
				if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == alias {
					findings = append(findings, positionFinding(fset, selector.Pos(), rel, "runtime."+selector.Sel.Name+" check"))
				}
				return true
			})
			return nil
		})
		contractcheck.FailErr(t, "scan "+root, err)
	}
	return findings
}

func platformFileSuffix(name string) bool {
	parts := strings.Split(strings.TrimSuffix(name, ".go"), "_")
	if len(parts) < 2 {
		return false
	}
	last := parts[len(parts)-1]
	return slices.Contains(platformOSNames, last) || slices.Contains(platformArchNames, last)
}

func platformBuildConstraint(file *ast.File) bool {
	for _, group := range file.Comments {
		if group.Pos() >= file.Package {
			break
		}
		for _, comment := range group.List {
			if !constraint.IsGoBuild(comment.Text) {
				continue
			}
			expr, err := constraint.Parse(comment.Text)
			if err != nil || platformConstraintTag(expr) {
				return true
			}
		}
	}
	return false
}

func platformConstraintTag(expr constraint.Expr) bool {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		return slices.Contains(platformOSNames, e.Tag) || slices.Contains(platformArchNames, e.Tag)
	case *constraint.NotExpr:
		return platformConstraintTag(e.X)
	case *constraint.AndExpr:
		return platformConstraintTag(e.X) || platformConstraintTag(e.Y)
	case *constraint.OrExpr:
		return platformConstraintTag(e.X) || platformConstraintTag(e.Y)
	}
	return false
}
