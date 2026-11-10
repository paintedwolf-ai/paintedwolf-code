package projectpaths_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestProjectPathBoundariesRefuseUnsafeAttachedRoot(t *testing.T) {
	for _, root := range refusedProjectRoots(t) {
		t.Run(root.name, func(t *testing.T) {
			tctx := tools.ToolContext{
				Source: tools.InvocationSource{Roots: []projectroot.RootRef{{
					ID: "root", Label: "root", Path: root.path, IsPrimary: true,
				}},
					ActiveRootID: "root"},
			}
			checks := map[string]func() error{
				"read": func() error {
					_, err := projectpaths.ResolveRead(context.Background(), nil, tctx, "tmp")
					return err
				},
				"write": func() error {
					_, err := projectpaths.ResolveWrite(context.Background(), nil, tctx, "tmp/file")
					return err
				},
				"union": func() error {
					_, err := projectpaths.UnionDiscoveryRoots(context.Background(), tctx, ".")
					return err
				},
				"command cwd": func() error {
					_, _, err := projectpaths.CommandCwd(context.Background(), tctx, "")
					return err
				},
			}
			for name, check := range checks {
				t.Run(name, func(t *testing.T) {
					err := check()
					var reject *toolrejection.ToolReject
					if !errors.As(err, &reject) || reject.Code != "SANDBOX_CAPABILITY_REQUEST_INVALID" {
						t.Fatalf("error = %v, want SANDBOX_CAPABILITY_REQUEST_INVALID ToolReject", err)
					}
					if reject.Data["reason"] != root.code || reject.Data["path"] != filepath.ToSlash(root.path) {
						t.Fatalf("reject data = %+v, want reason %q", reject.Data, root.code)
					}
				})
			}
		})
	}
}

func refusedProjectRoots(t *testing.T) []struct{ name, path, code string } {
	t.Helper()
	loopA := filepath.Join(t.TempDir(), "loop-a")
	loopB := filepath.Join(t.TempDir(), "loop-b")
	testutil.FailErr(t, "create loop a", os.Symlink(loopB, loopA))
	testutil.FailErr(t, "create loop b", os.Symlink(loopA, loopB))
	return []struct{ name, path, code string }{
		{"relative", "relative-project", confine.WriteRootCodeNotAbsolute},
		{"symlink-loop", loopA, confine.WriteRootCodeNotAbsolute},
	}
}

func TestAttachedBroadAndControlPlaneRootsEnforcePerTargetFloors(t *testing.T) {
	home := fspath.CanonicalPath(t.TempDir())
	dotConfig := filepath.Join(home, ".config")
	cfgDir := filepath.Join(dotConfig, "paintedwolf-state")
	siblingDir := filepath.Join(home, "workspace")
	siblingFile := filepath.Join(siblingDir, "notes.txt")
	stateFile := filepath.Join(cfgDir, "api.token")
	scratchDir := filepath.Join(cfgDir, "sessions", "s1", "scratch")
	outputDir := filepath.Join(cfgDir, "sessions", "s1", "output")
	draftDir := filepath.Join(cfgDir, "drafts", "d1")
	for _, dir := range []string{cfgDir, siblingDir, scratchDir, outputDir, draftDir} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o700))
	}
	testutil.FailErr(t, "write sibling", os.WriteFile(siblingFile, []byte("ok"), 0o600))
	testutil.FailErr(t, "write token", os.WriteFile(stateFile, []byte("secret"), 0o600))
	testutil.FailErr(t, "write draft", os.WriteFile(filepath.Join(draftDir, "draft.txt"), []byte("draft"), 0o600))
	testutil.FailErr(t, "write scratch", os.WriteFile(filepath.Join(scratchDir, "scratch.txt"), []byte("scratch"), 0o600))
	testutil.FailErr(t, "write output", os.WriteFile(filepath.Join(outputDir, "out.txt"), []byte("out"), 0o600))

	aliasToConfig := filepath.Join(home, "config-link")
	testutil.FailErr(t, "symlink config", os.Symlink(dotConfig, aliasToConfig))

	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(configdir.EnvConfigDir, cfgDir)

	homeCtx := tools.ToolContext{
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: "home", Label: "home", Path: home, IsPrimary: true}},
			ActiveRootID: "home",
		},
		Host: tools.InvocationHost{
			SessionScratchDir: scratchDir,
			SessionOutputDir:  outputDir,
		},
	}

	// Ordinary sibling read/write under attached $HOME succeeds.
	if _, err := projectpaths.ResolveRead(t.Context(), nil, homeCtx, "workspace/notes.txt"); err != nil {
		t.Fatalf("ResolveRead sibling under attached $HOME failed: %v", err)
	}
	sess := projectpaths.NewReadSession(nil, homeCtx)
	t.Cleanup(func() { sess.Close() })
	if _, err := sess.Resolve(t.Context(), "workspace/notes.txt"); err != nil {
		t.Fatalf("ReadSession.Resolve sibling under attached $HOME failed: %v", err)
	}
	if _, err := projectpaths.ResolveWrite(t.Context(), nil, homeCtx, "workspace/new.txt"); err != nil {
		t.Fatalf("ResolveWrite sibling under attached $HOME failed: %v", err)
	}

	// Control-plane file under attached $HOME is denied for both ResolveRead, ReadSession, and ResolveWrite.
	for _, call := range []struct {
		name string
		fn   func() error
	}{
		{"ResolveRead relative", func() error {
			_, err := projectpaths.ResolveRead(t.Context(), nil, homeCtx, ".config/paintedwolf-state/api.token")
			return err
		}},
		{"ReadSession.Resolve relative", func() error {
			_, err := sess.Resolve(t.Context(), ".config/paintedwolf-state/api.token")
			return err
		}},
		{"ResolveWrite relative", func() error {
			_, err := projectpaths.ResolveWrite(t.Context(), nil, homeCtx, ".config/paintedwolf-state/api.token")
			return err
		}},
		{"ResolveWrite ancestor root dot", func() error {
			_, err := projectpaths.ResolveWrite(t.Context(), nil, homeCtx, ".")
			return err
		}},
		{"ResolveWrite ancestor .config", func() error {
			_, err := projectpaths.ResolveWrite(t.Context(), nil, homeCtx, ".config")
			return err
		}},
		{"ResolveWrite ancestor symlink alias", func() error {
			_, err := projectpaths.ResolveWrite(t.Context(), nil, homeCtx, "config-link")
			return err
		}},
	} {
		err := call.fn()
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != isolation.CodeControlPlaneDenied {
			t.Fatalf("%s: err = %v, want %s", call.name, err, isolation.CodeControlPlaneDenied)
		}
	}

	// Managed workspaces, @scratch, and @output remain accessible under attached $HOME or configdir.Root().
	if _, err := projectpaths.ResolveRead(t.Context(), nil, homeCtx, ".config/paintedwolf-state/drafts/d1/draft.txt"); err != nil {
		t.Fatalf("ResolveRead managed draft under attached $HOME failed: %v", err)
	}
	if _, err := projectpaths.ResolveWrite(t.Context(), nil, homeCtx, ".config/paintedwolf-state/drafts/d1/draft.txt"); err != nil {
		t.Fatalf("ResolveWrite managed draft under attached $HOME failed: %v", err)
	}
	if _, err := sess.Resolve(t.Context(), "@scratch/scratch.txt"); err != nil {
		t.Fatalf("ReadSession.Resolve @scratch failed: %v", err)
	}
	if _, err := sess.Resolve(t.Context(), "@output/out.txt"); err != nil {
		t.Fatalf("ReadSession.Resolve @output failed: %v", err)
	}

	// CompileReadScope and SurveyWalk prune control-plane subtree under attached $HOME.
	b := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "default"}})
	scope, err := b.CompileReadScope(t.Context(), home, "default")
	testutil.FailErr(t, "CompileReadScope", err)
	if !scope.Filter("workspace/notes.txt", false) {
		t.Fatal("CompileReadScope filtered out ordinary sibling under attached $HOME")
	}
	if scope.Filter(".config/paintedwolf-state/api.token", false) {
		t.Fatal("CompileReadScope admitted control-plane file under attached $HOME")
	}
	if !scope.Filter(".config/paintedwolf-state/drafts/d1/draft.txt", false) {
		t.Fatal("CompileReadScope filtered out managed draft workspace under attached $HOME")
	}
}

func TestCommandCwdAndProgramPathsControlPlaneRootFallbackAndRefusal(t *testing.T) {
	home := fspath.CanonicalPath(t.TempDir())
	cfgDir := filepath.Join(home, ".config", "paintedwolf-state")
	cfgSub := filepath.Join(cfgDir, "sub")
	scratchDir := filepath.Join(cfgDir, "sessions", "s1", "scratch")
	for _, dir := range []string{cfgDir, cfgSub, scratchDir} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o700))
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(configdir.EnvConfigDir, cfgDir)

	cfgCtx := tools.ToolContext{
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: "cfg", Label: "cfg", Path: cfgDir, IsPrimary: true}},
			ActiveRootID: "cfg",
		},
		Host: tools.InvocationHost{
			SessionScratchDir: scratchDir,
		},
	}

	// 1. Omitted/defaulted cwd falls back to @scratch when active root is in control plane.
	for _, denyRead := range []string{"", "off"} {
		t.Run("omitted_cwd_deny_read_"+denyRead, func(t *testing.T) {
			if denyRead != "" {
				t.Setenv("LYCAON_SANDBOX_DENY_READ", denyRead)
			}
			abs, display, err := projectpaths.CommandCwd(t.Context(), cfgCtx, "")
			if err != nil {
				t.Fatalf("CommandCwd omitted cwd failed: %v", err)
			}
			if abs != scratchDir || display != "@scratch" {
				t.Fatalf("CommandCwd omitted cwd = (%q, %q), want (%q, \"@scratch\")", abs, display, scratchDir)
			}
			prog, err := projectpaths.NewProgramPaths(t.Context(), nil, cfgCtx, "")
			if err != nil {
				t.Fatalf("NewProgramPaths omitted cwd failed: %v", err)
			}
			held, err := prog.OpenCwd(t.Context())
			testutil.FailErr(t, "OpenCwd", err)
			_ = held.Close()
		})
	}

	// 2. Absent scratch fails closed with SANDBOX_CONTROL_PLANE_DENIED.
	noScratchCtx := cfgCtx
	noScratchCtx.Host.SessionScratchDir = ""
	if _, _, err := projectpaths.CommandCwd(t.Context(), noScratchCtx, ""); !isControlPlaneReject(err) {
		t.Fatalf("CommandCwd without scratch err = %v, want %s", err, isolation.CodeControlPlaneDenied)
	}
	if _, err := projectpaths.NewProgramPaths(t.Context(), nil, noScratchCtx, ""); !isControlPlaneReject(err) {
		t.Fatalf("NewProgramPaths without scratch err = %v, want %s", err, isolation.CodeControlPlaneDenied)
	}

	// 3. VerificationCheck refuses honestly with SANDBOX_CONTROL_PLANE_DENIED (never rewrites to @scratch).
	verifyCtx := cfgCtx
	verifyCtx.Execution.VerificationCheck = true
	if _, _, err := projectpaths.CommandCwd(t.Context(), verifyCtx, ""); !isControlPlaneReject(err) {
		t.Fatalf("CommandCwd verification err = %v, want %s", err, isolation.CodeControlPlaneDenied)
	}
	if _, err := projectpaths.NewProgramPaths(t.Context(), nil, verifyCtx, ""); !isControlPlaneReject(err) {
		t.Fatalf("NewProgramPaths verification err = %v, want %s", err, isolation.CodeControlPlaneDenied)
	}

	// 4. Explicit cwd into control plane refuses with SANDBOX_CONTROL_PLANE_DENIED even when LYCAON_SANDBOX_DENY_READ=off.
	for _, denyRead := range []string{"", "off"} {
		t.Run("explicit_cwd_deny_read_"+denyRead, func(t *testing.T) {
			if denyRead != "" {
				t.Setenv("LYCAON_SANDBOX_DENY_READ", denyRead)
			}
			for _, explicit := range []string{".", "sub"} {
				if _, _, err := projectpaths.CommandCwd(t.Context(), cfgCtx, explicit); !isControlPlaneReject(err) {
					t.Fatalf("CommandCwd(%q) err = %v, want %s", explicit, err, isolation.CodeControlPlaneDenied)
				}
				if _, err := projectpaths.NewProgramPaths(t.Context(), nil, cfgCtx, explicit); !isControlPlaneReject(err) {
					t.Fatalf("NewProgramPaths(%q) err = %v, want %s", explicit, err, isolation.CodeControlPlaneDenied)
				}
			}
		})
	}
}

func isControlPlaneReject(err error) bool {
	var reject *toolrejection.ToolReject
	return errors.As(err, &reject) && reject.Code == isolation.CodeControlPlaneDenied
}

