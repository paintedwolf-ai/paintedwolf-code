package osprocess

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestForbidigoRuleAndProductionSignalBoundary(t *testing.T) {
	moduleRoot := filepath.Clean(filepath.Join("..", ".."))
	golangci, err := os.ReadFile(filepath.Join(moduleRoot, ".golangci.yml"))
	if err != nil {
		t.Fatalf("read .golangci.yml: %v", err)
	}
	configText := string(golangci)
	for _, requiredPattern := range []string{
		`^syscall\.Kill$`,
		`^(golang\.org/x/sys/)?unix\.(Kill|Killpg|PidfdSendSignal)$`,
		`^os\.FindProcess$`,
	} {
		if !strings.Contains(configText, requiredPattern) {
			t.Fatalf(".golangci.yml missing forbidigo pattern %q", requiredPattern)
		}
	}

	// Verify the AST boundary detector catches both raw syscall.Kill and os.FindProcess.
	sampleSrc := `package sample
import (
	"os"
	"syscall"
)
func bad(pid int) {
	_ = syscall.Kill(pid, syscall.SIGTERM)
	_, _ = os.FindProcess(pid)
}
`
	fset := token.NewFileSet()
	sampleFile, err := parser.ParseFile(fset, "sample.go", sampleSrc, 0)
	if err != nil {
		t.Fatalf("parse sample: %v", err)
	}
	sampleFindings := scanForbiddenSignalCalls(fset, "internal/sample/sample.go", sampleFile)
	if len(sampleFindings) != 2 {
		t.Fatalf("sample findings = %v, want 2 violations (syscall.Kill and os.FindProcess)", sampleFindings)
	}

	var violations []string
	for _, root := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(moduleRoot, root), func(path string, d os.DirEntry, walkErr error) error {
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
			rel, relErr := filepath.Rel(moduleRoot, path)
			if relErr != nil {
				return relErr
			}
			slashRel := filepath.ToSlash(rel)
			if isAllowedLowLevelSignalPath(slashRel) {
				return nil
			}
			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			violations = append(violations, scanForbiddenSignalCalls(fset, slashRel, file)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if len(violations) != 0 {
		t.Fatalf("forbidden raw signal or FindProcess calls outside osprocess:\n  %s", strings.Join(violations, "\n  "))
	}
}

func isAllowedLowLevelSignalPath(slashRel string) bool {
	for _, prefix := range []string{
		"internal/osprocess/",
		"internal/launchguard/",
		"internal/confine/linuxsandbox/",
		"internal/hostprocess/",
	} {
		if strings.HasPrefix(slashRel, prefix) {
			return true
		}
	}
	switch slashRel {
	case "internal/exec/supervisor_linux.go",
		"internal/exec/supervisor_tree_linux.go",
		"internal/exec/relay_linux.go":
		return true
	default:
		return false
	}
}

func scanForbiddenSignalCalls(fset *token.FileSet, rel string, file *ast.File) []string {
	aliases := map[string]string{}
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		aliases[name] = importPath
	}
	var findings []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch aliases[pkg.Name] {
		case "syscall":
			if sel.Sel.Name == "Kill" {
				findings = append(findings, rel+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+": syscall.Kill")
			}
		case "golang.org/x/sys/unix":
			switch sel.Sel.Name {
			case "Kill", "Killpg", "PidfdSendSignal":
				findings = append(findings, rel+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+": unix."+sel.Sel.Name)
			}
		case "os":
			if sel.Sel.Name == "FindProcess" {
				findings = append(findings, rel+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+": os.FindProcess")
			}
		}
		return true
	})
	return findings
}
