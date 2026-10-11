package contract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func writeContractMinimalPack(t *testing.T, dir, id, ruleBody string) {
	t.Helper()
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(dir, "rules"), 0o700))
	testutil.FailErr(t, "pack.yaml", os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(
		"id: "+id+"\nlabel: L\ndescription: D\n",
	), 0o600))
	testutil.FailErr(t, "rule", os.WriteFile(filepath.Join(dir, "rules", "hit.yml"), []byte(ruleBody), 0o600))
}

func contractGoodRule(title string) string {
	return `title: ` + title + `
id: 33333333-3333-4333-8333-333333333331
description: consequence
logsource:
  product: lycaon
  service: tool_exec
level: high
detection:
  sel:
    Image: demobin
  condition: sel
`
}

func contractCriticalMatchAllRule() string {
	return `title: Everything
id: 77777777-7777-4777-8777-777777777777
description: matches any command
logsource:
  product: lycaon
  service: tool_exec
level: critical
detection:
  sel:
    CommandLine|contains: ' '
  condition: sel
`
}

func hashBundledDetectionPacks(t *testing.T) string {
	t.Helper()
	h := sha256.New()
	err := config.Walk(config.DetectionPacksDir, func(rel config.Rel, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			_, _ = io.WriteString(h, "D:"+rel.String()+"\n")
			return nil
		}
		data, err := config.Read(rel)
		if err != nil {
			return err
		}
		_, _ = io.WriteString(h, "F:"+rel.String()+"\n")
		_, _ = h.Write(data)
		return nil
	})
	testutil.FailErr(t, "hash bundled detection packs", err)
	return hex.EncodeToString(h.Sum(nil))
}

func filesOutsideDevicePacks(t *testing.T, home, configDir string) []string {
	t.Helper()
	deviceRoot := detectionpack.DevicePacksDir(configDir)
	var outside []string
	_ = filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		// Allow anything under the device packs directory (and the configDir root itself for state files).
		if strings.HasPrefix(path, deviceRoot+string(os.PathSeparator)) || path == deviceRoot {
			return nil
		}
		// Import never writes detection-packs.yaml; ignore empty dirs' parents.
		rel, _ := filepath.Rel(home, path)
		outside = append(outside, rel)
		return nil
	})
	return outside
}

// TestDetectionPackImportCannotEscape: hostile sources write nothing outside device packs dir.
func TestDetectionPackImportCannotEscape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		prep func(t *testing.T, src string)
	}{
		{
			name: "traversal id",
			prep: func(t *testing.T, src string) {
				writeContractMinimalPack(t, src, "ok-pack", contractGoodRule("Hit"))
				// Overwrite pack.yaml with a hostile id after writing a valid layout.
				testutil.FailErr(t, "hostile id", os.WriteFile(filepath.Join(src, "pack.yaml"), []byte(
					"id: ../escape\nlabel: L\ndescription: D\n",
				), 0o600))
			},
		},
		{
			name: "rules symlink to etc",
			prep: func(t *testing.T, src string) {
				writeContractMinimalPack(t, src, "sym-escape", contractGoodRule("Hit"))
				link := filepath.Join(src, "rules", "etc.yml")
				testutil.FailErr(t, "symlink", os.Symlink("/etc/passwd", link))
			},
		},
		{
			name: "dotdot folder name",
			prep: func(t *testing.T, src string) {
				// Source folder may be oddly named; destination uses validated id only.
				writeContractMinimalPack(t, src, "dotdot-pack", contractGoodRule("Hit"))
			},
		},
		{
			name: "deeply nested tree",
			prep: func(t *testing.T, src string) {
				writeContractMinimalPack(t, src, "deep-pack", contractGoodRule("Hit"))
				deep := filepath.Join(src, "a", "b", "c", "d")
				testutil.FailErr(t, "mkdir deep", os.MkdirAll(deep, 0o700))
				testutil.FailErr(t, "nested", os.WriteFile(filepath.Join(deep, "x.yml"), []byte("x"), 0o600))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			cfg := filepath.Join(home, "config")
			testutil.FailErr(t, "mkdir config", os.MkdirAll(cfg, 0o700))
			src := filepath.Join(t.TempDir(), "src")
			if tc.name == "dotdot folder name" {
				src = filepath.Join(t.TempDir(), "dotdot-named")
			}
			tc.prep(t, src)
			_, _ = detectionpack.ImportPack(cfg, shippedDetectionPacks(t), detectionpack.ImportRequest{SourcePath: src})
			if outside := filesOutsideDevicePacks(t, home, cfg); len(outside) > 0 {
				t.Fatalf("wrote outside device packs: %v", outside)
			}
		})
	}
}

// TestDetectionPackImportAllowlistHonored: only pack.yaml, rules/*.yml, fixtures.yaml copy.
func TestDetectionPackImportAllowlistHonored(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeContractMinimalPack(t, src, "allow-pack", contractGoodRule("Hit"))
	testutil.FailErr(t, "fixtures", os.WriteFile(filepath.Join(src, "fixtures.yaml"), []byte("fixtures: []\n"), 0o600))
	testutil.FailErr(t, "evil.sh", os.WriteFile(filepath.Join(src, "evil.sh"), []byte("#!/bin/sh\n"), 0o600))
	testutil.FailErr(t, ".git", os.MkdirAll(filepath.Join(src, ".git"), 0o700))
	testutil.FailErr(t, "node_modules", os.MkdirAll(filepath.Join(src, "node_modules", "x"), 0o700))
	testutil.FailErr(t, "blob", os.WriteFile(filepath.Join(src, "big.bin"), make([]byte, 5<<20), 0o600))

	_, err := detectionpack.ImportPack(cfg, shippedDetectionPacks(t), detectionpack.ImportRequest{SourcePath: src})
	testutil.FailErr(t, "ImportPack", err)

	dest := filepath.Join(detectionpack.DevicePacksDir(cfg), "allow-pack")
	var copied []string
	err = filepath.WalkDir(dest, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dest, path)
		copied = append(copied, filepath.ToSlash(rel))
		return nil
	})
	testutil.FailErr(t, "walk dest", err)
	if len(copied) == 0 {
		t.Fatal("import copied no allowlisted files")
	}
	for _, got := range copied {
		ok := got == "pack.yaml" || got == "fixtures.yaml" ||
			(strings.HasPrefix(got, "rules/") && strings.HasSuffix(got, ".yml") && !strings.Contains(got[len("rules/"):], "/"))
		if !ok {
			t.Fatalf("non-allowlisted file copied: %s (all=%v)", got, copied)
		}
	}
	for _, forbidden := range []string{"evil.sh", ".git", "node_modules", "big.bin"} {
		for _, got := range copied {
			if strings.Contains(got, forbidden) {
				t.Fatalf("copied forbidden %s via %s", forbidden, got)
			}
		}
	}
}

// TestDetectionPackImportAtomicOnFailure verifies the public failure path leaves
// no committed or staged pack behind.
func TestDetectionPackImportAtomicOnFailure(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeContractMinimalPack(t, src, "bad-pack", "not: valid: [[[")
	_, err := detectionpack.ImportPack(cfg, shippedDetectionPacks(t), detectionpack.ImportRequest{SourcePath: src})
	if err == nil {
		t.Fatal("expected invalid pack error")
	}
	deviceRoot := detectionpack.DevicePacksDir(cfg)
	if ents, err := os.ReadDir(deviceRoot); err == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".import-") || e.Name() == "bad-pack" {
				t.Fatalf("leftover after failed import: %s", e.Name())
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		testutil.FailErr(t, "ReadDir device packs", err)
	}
}

// TestDetectionPackImportBundledImmutable: bundled tree hashes identical after hostile imports.
func TestDetectionPackImportBundledImmutable(t *testing.T) {
	t.Parallel()
	// Bundled packs ship inside the binary, so there is no directory an import
	// could reach. Hash what the loader actually reads, before and after.
	before := hashBundledDetectionPacks(t)

	cfg := t.TempDir()
	// Hostile import attempts + remove of a bundled id.
	src := filepath.Join(t.TempDir(), "src")
	writeContractMinimalPack(t, src, "aws-cli", contractGoodRule("Hit"))
	_, _ = detectionpack.ImportPack(cfg, shippedDetectionPacks(t), detectionpack.ImportRequest{SourcePath: src, Replace: true})
	_ = detectionpack.RemoveDevicePack(cfg, shippedDetectionPacks(t), "aws-cli")
	_ = detectionpack.RemoveDevicePack(cfg, shippedDetectionPacks(t), "gcp-structured-actions")
	_ = detectionpack.RemoveDevicePack(cfg, shippedDetectionPacks(t), "azure-structured-actions")

	writeContractMinimalPack(t, src, "../escape", contractGoodRule("Hit"))
	testutil.FailErr(t, "hostile id rewrite", os.WriteFile(filepath.Join(src, "pack.yaml"), []byte(
		"id: ../escape\nlabel: L\ndescription: D\n",
	), 0o600))
	_, _ = detectionpack.ImportPack(cfg, shippedDetectionPacks(t), detectionpack.ImportRequest{SourcePath: src})

	after := hashBundledDetectionPacks(t)
	if before != after {
		t.Fatal("bundled detection-packs tree mutated by import/remove paths")
	}
}

// TestDetectionPackImportedStillAskOnly: imported critical match-all pack cannot allow/deny/suppress.
func TestDetectionPackImportedStillAskOnly(t *testing.T) {
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeContractMinimalPack(t, src, "user-critical", contractCriticalMatchAllRule())
	_, err := detectionpack.ImportPack(cfg, shippedDetectionPacks(t), detectionpack.ImportRequest{SourcePath: src})
	testutil.FailErr(t, "ImportPack", err)

	cat, err := detectionpack.LoadCatalog(detectionInput(t, cfg, ""))
	testutil.FailErr(t, "LoadCatalog", err)
	srcGate := detectionpack.NewGateSource(detectionpack.NewMatcher(cat))
	approvalGate := gateWithDetection(t, gate.PostureBalanced, srcGate)

	res, err := approvalGate.Evaluate(context.Background(), containedCommand("echo hi"))
	testutil.FailErr(t, "Evaluate", err)
	if res.Denied {
		t.Fatalf("imported pack must not deny: %+v", res)
	}
	if res.Required() && res.AutoApproved() {
		t.Fatalf("Required+AutoApproved: %+v", res)
	}

	res, err = approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
Files: []string{"/etc/hosts"},
},
Scope: hitl.ActionScope{
ProjectDir: t.TempDir(),
SessionID: "s1",
},
})
	testutil.FailErr(t, "Evaluate deny", err)
	if !res.Required() || res.Gate() != api.GateOutsideRootsWrite {
		t.Fatalf("native path escape must ask outside_roots (not a detection card): %+v", res)
	}
	if res.DetectionCitation != nil {
		t.Fatalf("path escape must not cite a detection: %+v", res.DetectionCitation)
	}
}

// TestDetectionPackImportOneValidationPathAST: only ImportPack calls commitImport; DryRun branches.
func TestDetectionPackImportOneValidationPathAST(t *testing.T) {
	t.Parallel()
	path := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "detectionpack", "import.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	testutil.FailErr(t, "ParseFile import.go", err)

	var importPack, commitImport *ast.FuncDecl
	commitCallers := map[string]int{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil {
			continue
		}
		switch fn.Name.Name {
		case "ImportPack":
			importPack = fn
		case "commitImport":
			commitImport = fn
		}
	}
	if importPack == nil || commitImport == nil {
		t.Fatal("ImportPack/commitImport not found")
	}

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if ok && id.Name == "commitImport" {
				commitCallers[fn.Name.Name]++
			}
			return true
		})
	}
	if len(commitCallers) != 1 || commitCallers["ImportPack"] == 0 {
		t.Fatalf("commitImport callers=%v want only ImportPack", commitCallers)
	}

	// DryRun must return before commitImport; commitImport must use copyFileFn.
	var sawDryRunBranch, sawCommitAfterDryRun bool
	ast.Inspect(importPack.Body, func(n ast.Node) bool {
		iff, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		// if req.DryRun { return ... }
		cond := iff.Cond
		sel, ok := cond.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "DryRun" {
			return true
		}
		sawDryRunBranch = true
		// Ensure the then-branch returns without calling commitImport.
		ast.Inspect(iff.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "commitImport" {
				t.Fatal("DryRun branch must not call commitImport")
			}
			return true
		})
		return true
	})
	if !sawDryRunBranch {
		t.Fatal("ImportPack missing DryRun branch")
	}
	ast.Inspect(importPack.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "commitImport" {
			sawCommitAfterDryRun = true
		}
		return true
	})
	if !sawCommitAfterDryRun {
		t.Fatal("ImportPack must call commitImport on the commit path")
	}

	var usesCopyFileFn bool
	ast.Inspect(commitImport.Body, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if ok && id.Name == "copyFileFn" {
			usesCopyFileFn = true
		}
		return true
	})
	if !usesCopyFileFn {
		t.Fatal("commitImport must use copyFileFn")
	}
}
