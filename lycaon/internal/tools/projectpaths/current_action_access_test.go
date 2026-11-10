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
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type mutationRecorder struct{ paths []string }

func (r *mutationRecorder) RecordPrimaryMutation(_ context.Context, _, path string) {
	r.paths = append(r.paths, path)
}

func TestApprovedExternalWriteDoesNotRecordAProjectRewindPath(t *testing.T) {
	project := t.TempDir()
	outside := filepath.Join(filepath.VolumeName(project)+string(filepath.Separator), "unattached", t.Name())
	target := filepath.Join(outside, "notes.txt")
	recorder := &mutationRecorder{}
	tc := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}},
			ActiveRootID:     "root",
			MutationRecorder: recorder},
		Files: tools.InvocationFiles{ApprovedFileAccess: []hitl.GrantedPathDelta{{Path: grantedpath.Normalize(target), Write: true}}},
	}
	_, err := projectpaths.ResolveWrite(t.Context(), nil, tc, target)
	testutil.FailErr(t, "resolve external write", err)
	if len(recorder.paths) != 0 {
		t.Fatalf("external write recorded primary paths: %v", recorder.paths)
	}
	_, err = projectpaths.ResolveWrite(t.Context(), nil, tc, "notes.txt")
	testutil.FailErr(t, "resolve project write", err)
	if len(recorder.paths) != 1 || recorder.paths[0] != "notes.txt" {
		t.Fatalf("project write not recorded: %v", recorder.paths)
	}
}

func TestCurrentActionFileAccessIsExactAndDirectional(t *testing.T) {
	project := t.TempDir()
	outside := filepath.Join(filepath.VolumeName(project)+string(filepath.Separator), "unattached", t.Name())
	target := filepath.Join(outside, "notes.txt")
	tc := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}},
			ActiveRootID: "root"},
		Files: tools.InvocationFiles{ApprovedFileAccess: []hitl.GrantedPathDelta{{Path: grantedpath.Normalize(target)}}},
	}
	_, err := projectpaths.ResolveRead(t.Context(), nil, tc, target)
	testutil.FailErr(t, "read approved path", err)
	if _, err := projectpaths.ResolveWrite(t.Context(), nil, tc, target); err == nil {
		t.Fatal("read approval authorized a write")
	}
	for _, path := range []string{outside, filepath.Join(target, "child"), filepath.Join(outside, "sibling")} {
		if _, err := projectpaths.ResolveRead(t.Context(), nil, tc, path); err == nil {
			t.Fatalf("exact approval authorized %q", path)
		}
	}

	writeOnlyTC := tc
	writeOnlyTC.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: grantedpath.Normalize(target), Write: true}}
	if _, err := projectpaths.ResolveWrite(t.Context(), nil, writeOnlyTC, target); err != nil {
		t.Fatalf("write approval did not authorize write: %v", err)
	}
	if _, err := projectpaths.ResolveRead(t.Context(), nil, writeOnlyTC, target); err == nil {
		t.Fatal("write-only approval authorized a read")
	}
}

func TestExactReadGrantUnderAttachedProtectedRootPreservesFloorForSiblingsAndControlPlane(t *testing.T) {
	home := fspath.CanonicalPath(t.TempDir())
	cfgDir := filepath.Join(home, ".paintedwolf")
	sshDir := filepath.Join(home, ".ssh")
	for _, dir := range []string{cfgDir, sshDir} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o700))
	}
	privKey := filepath.Join(sshDir, "id_ed25519")
	pubKey := filepath.Join(sshDir, "id_ed25519.pub")
	apiToken := filepath.Join(cfgDir, "api.token")
	testutil.FailErr(t, "write id_ed25519", os.WriteFile(privKey, []byte("PRIVATE"), 0o600))
	testutil.FailErr(t, "write id_ed25519.pub", os.WriteFile(pubKey, []byte("PUBLIC"), 0o644))
	testutil.FailErr(t, "write api.token", os.WriteFile(apiToken, []byte("TOKEN"), 0o600))

	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(configdir.EnvConfigDir, cfgDir)

	prevKeys := confine.KeyMaterialHomeRelPaths()
	confine.SetKeyMaterialPathsSource(func() []string { return []string{"~/.ssh/"} })
	t.Cleanup(func() {
		if len(prevKeys) == 0 {
			confine.SetKeyMaterialPathsSource(nil)
			return
		}
		confine.SetKeyMaterialPathsSource(func() []string { return prevKeys })
	})

	b := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "default"}, {ID: "implement"}})
	baseTC := tools.ToolContext{
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: "home", Label: "home", Path: home, IsPrimary: true}},
			ActiveRootID: "home",
		},
	}

	// 1. Unapproved read of key material under attached $HOME is rejected with ScopeError.
	var scopeErr *sandbox.ScopeError
	if _, err := projectpaths.ResolveRead(t.Context(), b, baseTC, ".ssh/id_ed25519"); !errors.As(err, &scopeErr) {
		t.Fatalf("unapproved ResolveRead(.ssh/id_ed25519) err = %v, want *sandbox.ScopeError", err)
	}
	unapprovedSess := projectpaths.NewReadSession(b, baseTC)
	t.Cleanup(func() { unapprovedSess.Close() })
	if _, err := unapprovedSess.Resolve(t.Context(), ".ssh/id_ed25519"); !errors.As(err, &scopeErr) {
		t.Fatalf("unapproved ReadSession.Resolve(.ssh/id_ed25519) err = %v, want *sandbox.ScopeError", err)
	}

	// 2. Write-only grant or tree grant does not authorize reading protected key material.
	for _, invalidGrant := range []hitl.GrantedPathDelta{
		{Path: privKey, Write: true},
		{Path: privKey, Tree: true},
		{Path: sshDir, Tree: true},
	} {
		tc := baseTC
		tc.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{invalidGrant}
		if _, err := projectpaths.ResolveRead(t.Context(), b, tc, ".ssh/id_ed25519"); !errors.As(err, &scopeErr) {
			t.Fatalf("invalid grant %+v authorized ResolveRead: %v", invalidGrant, err)
		}
	}

	// 3. Exact single-file read grant on id_ed25519 authorizes reading id_ed25519,
	// while sibling id_ed25519.pub and directory .ssh remain denied.
	grantedTC := baseTC
	grantedTC.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: privKey, Write: false}}
	if _, err := projectpaths.ResolveRead(t.Context(), b, grantedTC, ".ssh/id_ed25519"); err != nil {
		t.Fatalf("exact read grant ResolveRead(.ssh/id_ed25519) failed: %v", err)
	}
	grantedSess := projectpaths.NewReadSession(b, grantedTC)
	t.Cleanup(func() { grantedSess.Close() })
	if _, err := grantedSess.Resolve(t.Context(), ".ssh/id_ed25519"); err != nil {
		t.Fatalf("exact read grant ReadSession.Resolve(.ssh/id_ed25519) failed: %v", err)
	}
	if _, err := projectpaths.ResolveRead(t.Context(), b, grantedTC, ".ssh/id_ed25519.pub"); !errors.As(err, &scopeErr) {
		t.Fatalf("exact read grant leaked to sibling .ssh/id_ed25519.pub: %v", err)
	}
	if _, err := projectpaths.ResolveRead(t.Context(), b, grantedTC, ".ssh"); !errors.As(err, &scopeErr) {
		t.Fatalf("exact read grant authorized directory .ssh: %v", err)
	}

	// 4. Control-plane files remain SANDBOX_CONTROL_PLANE_DENIED even if an ApprovedFileAccess entry names them.
	ctrlTC := baseTC
	ctrlTC.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: apiToken, Write: false}}
	_, err := projectpaths.ResolveRead(t.Context(), b, ctrlTC, ".paintedwolf/api.token")
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != isolation.CodeControlPlaneDenied {
		t.Fatalf("control-plane ResolveRead with grant err = %v, want %s", err, isolation.CodeControlPlaneDenied)
	}
}

func TestCurrentActionFileAccessRejectsRetargetedSymlink(t *testing.T) {
	project, outside := t.TempDir(), t.TempDir()
	alias := filepath.Join(outside, "alias")
	first := filepath.Join(filepath.VolumeName(project)+string(filepath.Separator), "unattached", t.Name(), "first")
	second := filepath.Join(filepath.Dir(first), "second")
	testutil.FailErr(t, "create reviewed alias", os.Symlink(first, alias))
	target := filepath.Join(alias, "notes.txt")
	tc := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}},
			ActiveRootID: "root"},
		Files: tools.InvocationFiles{ApprovedFileAccess: []hitl.GrantedPathDelta{{Path: grantedpath.Normalize(target), Write: true}}},
	}
	testutil.FailErr(t, "remove reviewed alias", os.Remove(alias))
	testutil.FailErr(t, "retarget alias", os.Symlink(second, alias))
	if _, err := projectpaths.ResolveWrite(t.Context(), nil, tc, target); err == nil {
		t.Fatal("approval followed a changed symlink")
	}
}
