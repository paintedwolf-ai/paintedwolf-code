package contract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repoinfo"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func TestMultiRootEveryBootToolClassified(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	registered := toolfixture.BootRegisteredToolSet(t, reg)
	for name := range registered {
		if !multiRootClassified(name) {
			t.Errorf("boot tool %q is not classified — add it to multi_root in native-tools.yaml", name)
		}
	}
}

func TestMultiRootExemptionHygiene(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	registered := toolfixture.BootRegisteredToolSet(t, reg)
	for _, name := range multiRootProbeExemptNames() {
		reason, _ := multiRootProbeExemption(name)
		if strings.TrimSpace(reason) == "" {
			t.Errorf("multiRootProbeExempt[%q] missing reason", name)
		}
		cap := multiRootCapabilityOf(name)
		if cap != toolcontract.MultiRootPathConsume && cap != toolcontract.MultiRootDiscovery && cap != toolcontract.MultiRootCommand {
			t.Errorf("multiRootProbeExempt[%q] is capability %q — drop the stale exempt entry", name, cap)
		}
		if !registered[name] {
			t.Errorf("multiRootProbeExempt[%q] is not a boot-registered tool", name)
		}
	}
}

func TestMultiRootCapabilityDeclaredNamesAreBootTools(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	registered := toolfixture.BootRegisteredToolSet(t, reg)
	schemas := contractToolSchemas(t)
	for _, name := range toolcontract.MultiRootDeclaredNames() {
		if registered[name] {
			continue
		}
		if _, ok := schemas.Tools[name]; ok {
			continue
		}
		t.Errorf("multi_root names %q but it is neither boot-registered nor in tools/schemas", name)
	}
}

// Every path and discovery tool refuses an unknown root label with the same code.
// Each tool is invoked with the probe in its own schema, so a tool that resolves
// paths outside the shared resolver still fails.
func TestMultiRootPathDiscoveryToolsRejectUnknownLabel(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	schemas := contractToolSchemas(t)
	fix := newMultiRootFixture(t)
	ctx := context.Background()
	probe := "@missing/secondary-sentinel.txt"
	registered := toolfixture.BootRegisteredToolSet(t, reg)
	for name := range registered {
		cap := multiRootCapabilityOf(name)
		if cap != toolcontract.MultiRootPathConsume && cap != toolcontract.MultiRootDiscovery {
			continue
		}
		if _, ok := multiRootProbeExemption(name); ok {
			continue
		}
		schema := contractArgsSchemaFor(reg, schemas, name)
		args, planted := multiRootPlantArgsFromSchema(schema, probe, true)
		if !planted {
			t.Errorf("path/discovery tool %q has no path-shaped schema property — add multiRootProbeExempt with a reason or extend multiRootPathArgProps", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			_, err := reg.Run(ctx, name, args, fix.tctx("s1"))
			if err == nil {
				t.Fatalf("%s(%v) accepted an unknown root label", name, args)
			}
			if code := toolRejectCode(err); code != "UNKNOWN_ROOT_LABEL" {
				t.Fatalf("%s: code = %q want UNKNOWN_ROOT_LABEL (err=%v)", name, code, err)
			}
		})
	}
}

func TestMultiRootResolveReadUnknownLabel(t *testing.T) {
	t.Parallel()
	fix := newMultiRootFixture(t)
	_, err := projectpaths.ResolveRead(context.Background(), nil, fix.tctx("s1"), "@missing/secondary-sentinel.txt")
	if err == nil {
		t.Fatal("expected unknown label reject")
	}
	if code := toolRejectCode(err); code != "UNKNOWN_ROOT_LABEL" {
		t.Fatalf("err = %v code = %q want UNKNOWN_ROOT_LABEL", err, code)
	}
}

func TestMultiRootZeroRootsStructuredReject(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	schemas := contractToolSchemas(t)
	ctx := context.Background()
	tctx := tools.ToolContext{Agent: "implement", SessionID: "s1"}
	registered := toolfixture.BootRegisteredToolSet(t, reg)
	for name := range registered {
		cap := multiRootCapabilityOf(name)
		if cap != toolcontract.MultiRootPathConsume && cap != toolcontract.MultiRootDiscovery {
			continue
		}
		if _, ok := multiRootProbeExemption(name); ok {
			continue
		}
		schema := contractArgsSchemaFor(reg, schemas, name)
		args, planted := multiRootPlantArgsFromSchema(schema, ".", false)
		if !planted {
			t.Errorf("path/discovery tool %q has no path-shaped schema property", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			_, err := reg.Run(ctx, name, args, tctx)
			if err == nil {
				t.Fatalf("%s(%v) ran with no folder attached", name, args)
			}
			if code := toolRejectCode(err); code != "PROJECT_HAS_NO_ROOTS" {
				t.Fatalf("%s: err = %v code = %q want PROJECT_HAS_NO_ROOTS", name, err, code)
			}
		})
	}
	// The command family shares one cwd resolver, and this asserts that resolver
	// once rather than looping over tool names around a call that does not take
	// one. Their own no-folder behaviour is covered by the CommandCwd contract.
	t.Run("command_cwd_resolver", func(t *testing.T) {
		_, _, err := projectpaths.CommandCwd(context.Background(), tctx, "")
		if err == nil {
			t.Fatal("expected error at 0 roots")
		}
		if code := toolRejectCode(err); code != "PROJECT_HAS_NO_ROOTS" {
			t.Fatalf("err = %v code = %q want PROJECT_HAS_NO_ROOTS", err, code)
		}
	})
}

func TestMultiRootPathToolResolvesLabeledRoot(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	fix := newMultiRootFixture(t)
	ctx := context.Background()
	out, err := reg.Run(ctx, "read", map[string]any{"path": "@lycaon-den/secondary-sentinel.txt"}, fix.tctx("s1"))
	contractcheck.FailErr(t, "read labeled root", err)
	if !strings.Contains(out, "secondary") {
		t.Fatalf("read output = %q", out)
	}
}

func TestMultiRootPathToolRejectsUnknownLabel(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	fix := newMultiRootFixture(t)
	ctx := context.Background()
	_, err := reg.Run(ctx, "read", map[string]any{"path": "@missing/secondary-sentinel.txt"}, fix.tctx("s1"))
	if err == nil {
		t.Fatal("expected unknown label reject")
	}
	code := toolRejectCode(err)
	if code != "UNKNOWN_ROOT_LABEL" {
		t.Fatalf("err = %v code = %q want UNKNOWN_ROOT_LABEL", err, code)
	}
}

func TestMultiRootPathToolRejectsTraversal(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	fix := newMultiRootFixture(t)
	ctx := context.Background()
	_, err := reg.Run(ctx, "read", map[string]any{"path": "../outside.txt"}, fix.tctx("s1"))
	if err == nil {
		t.Fatal("expected traversal reject")
	}
}

func TestMultiRootBareRelativeUsesActiveRootOnly(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	fix := newMultiRootFixture(t)
	ctx := context.Background()
	_, err := reg.Run(ctx, "read", map[string]any{"path": "secondary-sentinel.txt"}, fix.tctx("s1"))
	if err == nil {
		t.Fatal("expected missing file on active primary root")
	}
}

func TestMultiRootDiscoveryUnionFindsBothRoots(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	fix := newMultiRootFixture(t)
	ctx := context.Background()
	out, err := reg.Run(ctx, "find", map[string]any{
		"path":      ".",
		"name_glob": "*-sentinel.txt",
	}, fix.tctx("s1"))
	contractcheck.FailErr(t, "find union", err)
	if !strings.Contains(out, "primary-sentinel.txt") || !strings.Contains(out, "@lycaon-den") {
		t.Fatalf("find union output = %q", out)
	}
}

func TestMultiRootDiscoveryUnionListDir(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	fix := newMultiRootFixture(t)
	ctx := context.Background()
	out, err := reg.Run(ctx, "list_dir", map[string]any{"path": ".", "max_depth": 1}, fix.tctx("s1"))
	contractcheck.FailErr(t, "list_dir union", err)
	if !strings.Contains(out, "primary-sentinel.txt") || !strings.Contains(out, "@lycaon-den") {
		t.Fatalf("list_dir union output = %q", out)
	}
}

func TestMultiRootDiscoveryUnionGrep(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	fix := newMultiRootFixture(t)
	ctx := context.Background()
	out, err := reg.Run(ctx, "grep", map[string]any{
		"pattern": "secondary",
		"path":    ".",
	}, fix.tctx("s1"))
	contractcheck.FailErr(t, "grep union", err)
	if !strings.Contains(out, "secondary-sentinel.txt") {
		t.Fatalf("grep union output = %q", out)
	}
}

func TestMultiRootInvariantI9_AnalyzeRootsParity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		contractcheck.FailErr(t, "write main.go", err)
	}
	ctx := context.Background()
	provider := repotest.NewProvider(t)
	defer func() { _ = provider.Close() }()
	mrb, err := repoinfo.AnalyzeRoots(ctx, []projectroot.RootRef{{Path: dir, IsPrimary: true}}, repoinfo.DefaultBriefBudget(), provider)
	contractcheck.FailErr(t, "AnalyzeRoots", err)
	if len(mrb.Roots) != 1 || mrb.Roots[0].Brief == nil {
		t.Fatalf("mrb = %+v", mrb)
	}
	mrb2, err := repoinfo.AnalyzeRoots(ctx, []projectroot.RootRef{{Path: dir, IsPrimary: true, Label: "x"}}, repoinfo.BriefBudget{TotalBytes: 4096, PrimaryShare: 0.6}, provider)
	contractcheck.FailErr(t, "AnalyzeRoots labeled", err)
	if mrb.PrimaryRepoBrief().FileCount != mrb2.PrimaryRepoBrief().FileCount {
		t.Fatalf("file counts differ: %d vs %d", mrb.PrimaryRepoBrief().FileCount, mrb2.PrimaryRepoBrief().FileCount)
	}
}

func TestMultiRootResolverQualifyRoundTrip(t *testing.T) {
	t.Parallel()
	fix := newMultiRootFixture(t)
	abs, root, err := projectroot.ResolveAbs(fix.roots, fix.activeID, "@lycaon-den/secondary-sentinel.txt")
	contractcheck.FailErr(t, "ResolveAbs", err)
	primary, err := projectroot.PrimaryRoot(fix.roots)
	contractcheck.FailErr(t, "PrimaryRoot", err)
	display := projectroot.Qualify(primary, root, abs)
	if display != "@lycaon-den/secondary-sentinel.txt" {
		t.Fatalf("Qualify = %q", display)
	}
	abs2, _, err := projectroot.ResolveAbs(fix.roots, fix.activeID, display)
	contractcheck.FailErr(t, "ResolveAbs round trip", err)
	if abs2 != abs {
		t.Fatalf("round trip abs = %q want %q", abs2, abs)
	}
}

func TestMultiRootPathsArrayToolStat(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	fix := newMultiRootFixture(t)
	ctx := context.Background()
	out, err := reg.Run(ctx, "stat", map[string]any{
		"paths": []any{"@lycaon-den/secondary-sentinel.txt"},
	}, fix.tctx("s1"))
	contractcheck.FailErr(t, "stat labeled root", err)
	if !strings.Contains(out, "secondary-sentinel.txt") {
		t.Fatalf("stat output = %q", out)
	}
}

func TestMultiRootAnalyzeRootsBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := t.TempDir()
	primary := filepath.Join(base, "a")
	secondary := filepath.Join(base, "b")
	for _, dir := range []string{primary, secondary} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			contractcheck.FailErr(t, "mkdir", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "file.go"), []byte("package x\n"), 0o644); err != nil {
			contractcheck.FailErr(t, "write", err)
		}
	}
	budget := repoinfo.BriefBudget{TotalBytes: 2048, PrimaryShare: 0.6}
	provider := repotest.NewProvider(t)
	defer func() { _ = provider.Close() }()
	mrb, err := repoinfo.AnalyzeRoots(ctx, []projectroot.RootRef{
		{Path: primary, IsPrimary: true, Label: "a"},
		{Path: secondary, Label: "b"},
	}, budget, provider)
	contractcheck.FailErr(t, "AnalyzeRoots", err)
	if mrb.RenderedBytes() > budget.TotalBytes+256 {
		t.Fatalf("rendered %d exceeds budget %d", mrb.RenderedBytes(), budget.TotalBytes)
	}
}
