package contract

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func loadBundledCoordinatorProfile(t *testing.T) sandbox.ToolProfile {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	for _, p := range profiles {
		if p.ID == "coordinator" {
			return p
		}
	}
	t.Fatal("coordinator profile missing")
	return sandbox.ToolProfile{}
}

func TestCoordinatorProfileAllowsNativeDiscoveryPrograms(t *testing.T) {
	prof := loadBundledCoordinatorProfile(t)
	b := loadBundledSandboxBoundary(t)
	if !prof.Tools["command"] {
		t.Fatal("coordinator must grant the command entry")
	}
	contractcheck.FailErr(t, "admit coordinator command", b.AssertToolAllowed(t.Context(), "coordinator", "command", sandbox.ToolAccessProfile))
	readOnly, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	contractcheck.FailErr(t, "declare discovery effect", err)
	if !prof.CommandEffects.Allows(readOnly) {
		t.Fatal("coordinator must admit read-only program operations")
	}
	root := t.TempDir()
	contractcheck.FailErr(t, "seed discovery input", os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600))
	out, err := toolfixture.NewProgramFixture(t).Invoke(t, "find . -name '*.go'; rg package .", tools.ToolContext{
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Label: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root"},
		Identity: tools.InvocationIdentity{Agent: "coordinator"},
		Turn:     tools.InvocationTurn{TurnSurfaceID: toolcontract.SurfaceImplementInvestigate},
	})
	contractcheck.FailErr(t, "run coordinator discovery program", err)
	if out.Status != 0 || out.Refusal != nil || !strings.Contains(out.Stdout, "main.go") || !strings.Contains(out.Stdout, "package main") {
		t.Fatalf("discovery program failed: %+v", out)
	}
}

func TestCoordinatorProfileToolsIncludeCommandDiscovery(t *testing.T) {
	exec := toolfixture.ContractToolExecutor(t)
	names := toolfixture.SortedToolNames(context.Background(), exec.Metadata, "coordinator")
	// Profile grant; the turn surface gates invoke.
	for _, want := range []string{"command"} {
		if !slices.Contains(names, want) {
			t.Fatalf("coordinator profile schema must include %q; got %v", want, names)
		}
	}
	for _, retired := range []string{"find", "grep", "read"} {
		if slices.Contains(names, retired) {
			t.Fatalf("retired operation %s exposed as a public tool", retired)
		}
	}
}

func TestCoordinatorReadsProductAndPlanPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, rel := range []string{"src/main.go", settingsoverlay.Rel("blueprints/game.md")} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		contractcheck.FailErr(t, "create directory", os.MkdirAll(filepath.Dir(path), 0o755))
		contractcheck.FailErr(t, "write file", os.WriteFile(path, []byte("content\n"), 0o644))
	}
	b := loadBundledSandboxBoundary(t)
	tctx := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "coordinator"},
	}
	for _, rel := range []string{"src/main.go", settingsoverlay.Rel("blueprints/game.md")} {
		resolved, err := projectpaths.ResolveRead(context.Background(), b, tctx, rel)
		contractcheck.FailErr(t, "coordinator read "+rel, err)
		if resolved.Abs != filepath.Join(root, filepath.FromSlash(rel)) {
			t.Fatalf("coordinator read %s resolved to %s", rel, resolved.Abs)
		}
	}
}

func loadBundledSandboxBoundary(t *testing.T) *sandbox.Boundary {
	t.Helper()
	cfg, err := sandbox.LoadConfig()
	contractcheck.FailErr(t, "load sandbox config", err)
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	return sandbox.NewBoundary(cfg, profiles)
}
