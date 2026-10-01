package contract

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// retiredCheckoutDirs are checkout-local output directories tooling must not
// address; scripts/artifact_paths.py resolves the out-of-tree roots instead.
var retiredCheckoutDirs = []string{".bin", ".task"}

const retiredArtifactRule = "repository tooling must address build and test outputs through scripts/artifact_paths.py " +
	"(PW_ARTIFACT_ROOT, PW_BUILD_DIR, PW_BIN_DIR, PW_LOCK_ROOT), never a checkout-relative .bin/ or .task/ path; " +
	"resolve the directory with `python3 scripts/artifact_paths.py {artifacts|build|bin|locks} <root>`, " +
	"source scripts/artifact-paths.sh, or import artifact_paths"

// retiredSegment matches a retired directory as a whole path segment.
var retiredSegment = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])(\./)?\.(bin|task)(/|["'\s)]|$)`)

// checkoutAnchors are prefixes that denote the checkout root in workflows,
// Taskfiles, and shell.
var checkoutAnchors = regexp.MustCompile(`(\$\{?GITHUB_WORKSPACE\}?|\$\{\{\s*github\.workspace\s*\}\}|\{\{\s*\.(ROOT_DIR|TASKFILE_DIR|USER_WORKING_DIR)\s*\}\}|\$\{?PWD\}?|\$\(pwd\))/$`)

// shellRootAssignment finds shell variables assigned the checkout root.
var shellRootAssignment = regexp.MustCompile(`(?m)^\s*(?:export\s+|local\s+|readonly\s+)?([A-Za-z_][A-Za-z0-9_]*)=["']?(.*)$`)

var trailingShellVariable = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?/$`)

func TestRepositoryToolingUsesResolvedArtifactPaths(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	raw, err := exec.CommandContext(t.Context(), "git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z").Output()
	contractcheck.FailErr(t, "list checkout files", err)
	var text, python, golang []string
	for _, rel := range strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00") {
		if !retiredScanScope(rel) {
			continue
		}
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil || !info.Mode().IsRegular() {
			continue
		}
		switch base := path.Base(rel); {
		case strings.HasSuffix(rel, ".py"):
			python = append(python, rel)
		case strings.HasSuffix(rel, ".go"):
			golang = append(golang, rel)
		case strings.HasPrefix(rel, ".github/"), strings.HasSuffix(rel, ".sh"), strings.HasSuffix(rel, ".bash"),
			base == ".envrc", base == "task", strings.HasPrefix(base, "Taskfile") && (strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml")),
			strings.HasPrefix(rel, "scripts/") && path.Ext(rel) == "":
			text = append(text, rel)
		}
	}
	if len(text) == 0 || len(python) == 0 || len(golang) == 0 {
		t.Fatalf("scan found %d text, %d Python, %d Go files; the inventory is broken", len(text), len(python), len(golang))
	}
	var violations []string
	for _, rel := range text {
		violations = append(violations, retiredTextReferences(rel, contractcheck.ReadRepoFile(t, root, rel))...)
	}
	for _, rel := range golang {
		violations = append(violations, retiredGoReferences(t, root, rel)...)
	}
	violations = append(violations, retiredPythonReferences(t, root, python)...)
	contractcheck.FailViolations(t, retiredArtifactRule, violations)
}

// retiredScanScope keeps first-party tooling and product code; tests, fixtures,
// vendored trees, and prose are out of scope.
func retiredScanScope(rel string) bool {
	for _, segment := range strings.Split(rel, "/") {
		if shellInventoryExcludedSegments[segment] {
			return false
		}
	}
	base := path.Base(rel)
	switch {
	case strings.HasSuffix(base, "_test.go"), strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"),
		strings.Contains(rel, "/verification_tests/"), strings.HasSuffix(rel, ".md"):
		return false
	}
	return true
}

func retiredTextReferences(rel, body string) []string {
	roots := map[string]bool{}
	for _, match := range shellRootAssignment.FindAllStringSubmatch(body, -1) {
		if value := match[2]; strings.Contains(value, "--show-toplevel") ||
			strings.Contains(value, "dirname") && strings.Contains(value, "/..") && strings.Contains(value, "pwd") {
			roots[match[1]] = true
		}
	}
	var violations []string
	for index, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, loc := range retiredSegment.FindAllStringSubmatchIndex(line, -1) {
			start := loc[3]
			prefix := line[:start]
			anchored := start == 0 || !strings.HasSuffix(prefix, "/")
			if !anchored {
				anchored = checkoutAnchors.MatchString(prefix)
			}
			if !anchored {
				if variable := trailingShellVariable.FindStringSubmatch(prefix); variable != nil {
					anchored = roots[variable[1]]
				}
			}
			if anchored {
				violations = append(violations, rel+":"+strconv.Itoa(index+1)+": "+strings.TrimSpace(line))
			}
		}
	}
	return violations
}

// retiredGoReferences flags path joins with a retired segment in non-test Go.
func retiredGoReferences(t *testing.T, root, rel string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var violations []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || contractcheck.CallFuncName(call.Fun) != "Join" {
			return true
		}
		for _, arg := range call.Args {
			lit, ok := arg.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(lit.Value)
			if err == nil && slices.Contains(retiredCheckoutDirs, strings.SplitN(path.Clean(value), "/", 2)[0]) {
				violations = append(violations, rel+":"+strconv.Itoa(fset.Position(lit.Pos()).Line)+": joins "+lit.Value)
			}
		}
		return true
	})
	return violations
}

// retiredPythonAnalyzer reports `<checkout path> / '.bin...'` and
// os.path.join(<checkout path>, '.bin...') where the checkout path derives
// from __file__, the working directory, or a sibling module's such constant.
const retiredPythonAnalyzer = `
import ast, json, sys
from pathlib import Path

retired = set(json.loads(sys.argv[1]))
files = [Path(p) for p in sys.argv[2:]]
trees = {}
for file in files:
    try:
        trees[file] = ast.parse(file.read_text(encoding="utf-8"), filename=str(file))
    except (SyntaxError, UnicodeDecodeError):
        pass

def mentions(node, names):
    for child in ast.walk(node):
        if isinstance(child, ast.Name) and (child.id == "__file__" or child.id in names):
            return True
        if isinstance(child, ast.Attribute) and child.attr in ("cwd", "getcwd"):
            return True
    return False

def checkout_names(tree, imported):
    names = set(imported)
    changed = True
    while changed:
        changed = False
        for node in ast.walk(tree):
            if isinstance(node, ast.Assign) and mentions(node.value, names):
                for target in node.targets:
                    if isinstance(target, ast.Name) and target.id not in names:
                        names.add(target.id)
                        changed = True
    return names

exported = {}
for file, tree in trees.items():
    exported[file] = checkout_names(tree, set())

def first_segment(node):
    if isinstance(node, ast.Constant) and isinstance(node.value, str):
        return node.value.strip("/").split("/")[0]
    return None

violations = []
for file, tree in trees.items():
    imported = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.ImportFrom) and node.module and node.level == 0:
            sibling = file.parent / (node.module.replace(".", "/") + ".py")
            for alias in node.names:
                if alias.name in exported.get(sibling, ()):
                    imported.add(alias.asname or alias.name)
    names = checkout_names(tree, imported)
    for node in ast.walk(tree):
        hit = None
        if isinstance(node, ast.BinOp) and isinstance(node.op, ast.Div) and first_segment(node.right) in retired and mentions(node.left, names):
            hit = node.right
        elif isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) and node.func.attr == "join" and node.args and mentions(node.args[0], names):
            hit = next((arg for arg in node.args[1:] if first_segment(arg) in retired), None)
        if hit is not None:
            violations.append(f"{file}:{hit.lineno}: {ast.unparse(node)}")
print(json.dumps(violations))
`

func retiredPythonReferences(t *testing.T, root string, files []string) []string {
	t.Helper()
	retired, err := json.Marshal(retiredCheckoutDirs)
	contractcheck.FailErr(t, "encode retired directories", err)
	cmd := exec.CommandContext(t.Context(), "python3", append([]string{"-c", retiredPythonAnalyzer, string(retired)}, files...)...)
	cmd.Dir = root
	out, err := cmd.Output()
	contractcheck.FailErr(t, "analyze Python tooling", err)
	var violations []string
	contractcheck.FailErr(t, "decode Python analysis", json.Unmarshal(out, &violations))
	return violations
}
