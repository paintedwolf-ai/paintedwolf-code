package projectpaths_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// canon mirrors the canonical form resolveGranted hands the access source.
func canon(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return resolved
}

// grantEverything returns exact authority for each requested path.
func grantEverything(t *testing.T, root string) {
	t.Helper()
	canonRoot := canon(t, root)
	projectpaths.SetGrantedAccessSource(func(_, _, abs string, _ bool) (projectpaths.Access, bool) {
		if abs != canonRoot && !strings.HasPrefix(abs, canonRoot+string(filepath.Separator)) {
			return projectpaths.Access{}, false
		}
		return projectpaths.Access{Path: abs}, true
	})
	t.Cleanup(func() { projectpaths.SetGrantedAccessSource(nil) })
}

func TestGrantedAccessCannotReachProtectedSinks(t *testing.T) {
	outside := t.TempDir()
	proj := t.TempDir()
	grantEverything(t, outside)

	tctx := tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "p", Label: "proj", Path: proj, IsPrimary: true}},
		ActiveRootID: "p",
		SessionID:    "chat-1",
	}

	for name, path := range map[string]string{
		"git internals config": filepath.Join(outside, ".git", "config"),
		"git hooks":            filepath.Join(outside, ".git", "hooks", "pre-commit"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := projectpaths.ResolveWrite(context.Background(), nil, tctx, path); err == nil {
				t.Fatalf("a grant must not open %s (%s)", name, path)
			}
		})
	}
}

func TestGrantedAccessStillResolvesOrdinaryPaths(t *testing.T) {
	outside := t.TempDir()
	proj := t.TempDir()
	grantEverything(t, outside)

	tctx := tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "p", Label: "proj", Path: proj, IsPrimary: true}},
		ActiveRootID: "p",
		SessionID:    "chat-1",
	}
	target := filepath.Join(outside, "notes", "todo.md")
	want := filepath.Join(canon(t, outside), "notes", "todo.md")
	res, err := projectpaths.ResolveWrite(context.Background(), nil, tctx, target)
	testutil.FailErr(t, "ResolveWrite granted path", err)
	if res.Abs != want {
		t.Fatalf("abs = %q want %q", res.Abs, want)
	}
	if !res.External {
		t.Fatal("a path resolved through a grant must be marked Granted")
	}
	if res.Root.Path == proj {
		t.Fatal("a granted path must not claim an attached root")
	}
}

func TestTreeGrantResolvesDescendantsAndExactDoesNot(t *testing.T) {
	folder := filepath.Join(filepath.VolumeName(os.TempDir())+string(filepath.Separator), "unattached", t.Name())
	proj := t.TempDir()
	child := filepath.Join(folder, "release.go")
	nested := filepath.Join(folder, "pkg", "client.go")
	sibling := filepath.Join(filepath.Dir(folder), "other.go")

	canonFolder := canon(t, folder)
	projectpaths.SetGrantedAccessSource(func(_, _, abs string, write bool) (projectpaths.Access, bool) {
		if write {
			return projectpaths.Access{}, false
		}
		if abs == canonFolder || strings.HasPrefix(abs, canonFolder+string(filepath.Separator)) {
			return projectpaths.Access{Path: canonFolder, Tree: true}, true
		}
		return projectpaths.Access{}, false
	})
	t.Cleanup(func() { projectpaths.SetGrantedAccessSource(nil) })

	tctx := tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "p", Label: "proj", Path: proj, IsPrimary: true}},
		ActiveRootID: "p",
		SessionID:    "chat-1",
	}
	for _, path := range []string{folder, child, nested} {
		res, err := projectpaths.ResolveRead(context.Background(), nil, tctx, path)
		testutil.FailErr(t, "ResolveRead "+path, err)
		if !res.External || res.Root.Path != canonFolder {
			t.Fatalf("%s resolved through tree grant = %+v", path, res)
		}
	}
	if _, err := projectpaths.ResolveRead(context.Background(), nil, tctx, sibling); err == nil {
		t.Fatal("a sibling outside the tree must not resolve")
	}

	canonChild := canon(t, child)
	projectpaths.SetGrantedAccessSource(func(_, _, abs string, _ bool) (projectpaths.Access, bool) {
		if abs != canonChild {
			return projectpaths.Access{}, false
		}
		return projectpaths.Access{Path: canonChild}, true
	})
	if _, err := projectpaths.ResolveRead(context.Background(), nil, tctx, nested); err == nil {
		t.Fatal("an exact file grant must not resolve a descendant")
	}
	res, err := projectpaths.ResolveRead(context.Background(), nil, tctx, child)
	testutil.FailErr(t, "ResolveRead exact file", err)
	if !res.External {
		t.Fatal("the exact granted file must still resolve")
	}
}

func TestGrantedAccessKeepsGovernanceReadable(t *testing.T) {
	outside := t.TempDir()
	proj := t.TempDir()
	grantEverything(t, outside)
	tctx := tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "p", Label: "proj", Path: proj, IsPrimary: true}},
		ActiveRootID: "p",
		SessionID:    "chat-1",
	}
	// Policy files remain readable.
	if _, err := projectpaths.ResolveRead(context.Background(), nil, tctx,
		filepath.Join(outside, "AGENTS.md")); err != nil {
		t.Fatalf("reading policy through a grant should resolve: %v", err)
	}
}

// The state tree refuses grants with the code the approval gate reports, in
// both lanes. Sinks keep their own write code, and the agent workspaces the
// engine manages under the tree resolve through a grant like any other path.
func TestControlPlanePathsRefuseGrantsWithTheGateCode(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	proj := t.TempDir()
	grantEverything(t, cfg)

	tctx := tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "p", Label: "proj", Path: proj, IsPrimary: true}},
		ActiveRootID: "p",
		SessionID:    "chat-1",
	}
	expectCode := func(t *testing.T, err error, code string) {
		t.Helper()
		var reject *toolrejection.ToolReject
		if err == nil || !errors.As(err, &reject) || reject.Code != code {
			t.Fatalf("err = %v want %s", err, code)
		}
	}
	for name, path := range map[string]string{
		"debug sessions":  filepath.Join(cfg, "debug", "sessions", "sidecar.log"),
		"installed skill": filepath.Join(cfg, "packs", "stock", "skills", "verify-a-change", "SKILL.md"),
		"skill reference": filepath.Join(cfg, "skills", "demo", "references", "r.md"),
		"host database":   filepath.Join(cfg, "store.db"),
		"host WAL":        filepath.Join(cfg, "store.db-wal"),
		"host SHM":        filepath.Join(cfg, "store.db-shm"),
		"host approvals":  filepath.Join(cfg, "approvals.yaml"),
		"host mcp":        filepath.Join(cfg, "mcp.yaml"),
		"host creds":      filepath.Join(cfg, "credential-vault.age"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := projectpaths.ResolveRead(context.Background(), nil, tctx, path)
			expectCode(t, err, isolation.CodeControlPlaneDenied)
			_, err = projectpaths.ResolveWrite(context.Background(), nil, tctx, path)
			expectCode(t, err, isolation.CodeControlPlaneDenied)
		})
	}

	draft := filepath.Join(enginepaths.DraftsRootUnder(cfg), "p", "notes.md")
	testutil.FailErr(t, "mkdir draft", os.MkdirAll(filepath.Dir(draft), 0o755))
	res, err := projectpaths.ResolveRead(context.Background(), nil, tctx, draft)
	testutil.FailErr(t, "ResolveRead draft through grant", err)
	if !res.External {
		t.Fatal("an agent workspace under the state tree must resolve through its grant")
	}
}
