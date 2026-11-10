package projectpaths_test

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
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

func TestCurrentActionFileAccessFrozenSubjectRejectsLeafAndAncestorReplacement(t *testing.T) {
	project := grantedpath.Normalize(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	testutil.FailErr(t, "resolve home for external fixture", err)
	rawOutside, err := os.MkdirTemp(account.HomeDir, ".file-access-frozen-test-") //nolint:usetesting // Outside the permitted temporary roots, including the test HOME.
	testutil.FailErr(t, "create external fixture", err)
	t.Cleanup(func() { testutil.FailErr(t, "remove external fixture", os.RemoveAll(rawOutside)) })
	outside := grantedpath.Normalize(rawOutside)

	subA := filepath.Join(outside, "sub-a")
	subB := filepath.Join(outside, "sub-b")
	testutil.FailErr(t, "mkdir sub-a", os.MkdirAll(subA, 0o700))
	testutil.FailErr(t, "mkdir sub-b", os.MkdirAll(subB, 0o700))

	fileA := filepath.Join(subA, "a.txt")
	fileB := filepath.Join(subB, "b.txt")
	testutil.FailErr(t, "write fileA", os.WriteFile(fileA, []byte("alpha\n"), 0o600))
	testutil.FailErr(t, "write fileB", os.WriteFile(fileB, []byte("bravo\n"), 0o600))

	frozenA := grantedpath.Normalize(fileA)
	aliasLeaf := filepath.Join(outside, "alias-a.txt")
	aliasDir := filepath.Join(outside, "alias-sub-a")
	testutil.FailErr(t, "symlink aliasLeaf -> fileA", os.Symlink(fileA, aliasLeaf))
	testutil.FailErr(t, "symlink aliasDir -> subA", os.Symlink(subA, aliasDir))

	baseTC := tools.ToolContext{
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}},
			ActiveRootID: "root",
		},
	}

	makeCarriers := func(frozenPath string, write bool) []struct {
		name    string
		tc      tools.ToolContext
		install func() func()
	} {
		approvedTC := baseTC
		approvedTC.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: frozenPath, Write: write}}

		preparedTC := baseTC
		preparedTC.Files.PreparedFileAccess = []hitl.GrantedPathDelta{{Path: frozenPath, Write: write}}
		preparedTC.Files.FileChangeReview = func(context.Context, []tools.FileChange) error { return nil }

		sessionTC := baseTC
		sessionTC.Files.SessionReadPaths = []string{frozenPath}

		sourceTC := baseTC
		return []struct {
			name    string
			tc      tools.ToolContext
			install func() func()
		}{
			{name: "ApprovedFileAccess", tc: approvedTC},
			{name: "PreparedFileAccess", tc: preparedTC},
			{name: "SessionReadPaths", tc: sessionTC},
			{
				name: "GrantedAccessSource",
				tc:   sourceTC,
				install: func() func() {
					return projectpaths.SetGrantedAccessSource(func(_, _, _ string, w bool) (projectpaths.Access, bool) {
						if w != write {
							return projectpaths.Access{}, false
						}
						return projectpaths.Access{Path: frozenPath, Tree: false}, true
					})
				},
			},
		}
	}

	// 1. Positive: exact target and aliases resolving to the unchanged approved target succeed.
	for _, carrier := range makeCarriers(frozenA, false) {
		var release func()
		if carrier.install != nil {
			release = carrier.install()
		}
		for _, candidate := range []string{fileA, aliasLeaf, filepath.Join(aliasDir, "a.txt")} {
			res, err := projectpaths.ResolveRead(t.Context(), nil, carrier.tc, candidate)
			if err != nil {
				t.Fatalf("%s: ResolveRead(%q) on unchanged target failed: %v", carrier.name, candidate, err)
			}
			if res.Abs != frozenA {
				t.Fatalf("%s: ResolveRead(%q).Abs = %q, want %q", carrier.name, candidate, res.Abs, frozenA)
			}
		}
		if release != nil {
			release()
		}
	}

	// 2. Approved-leaf replacement: replace fileA itself with a symlink to fileB.
	testutil.FailErr(t, "remove fileA", os.Remove(fileA))
	testutil.FailErr(t, "replace fileA with symlink to fileB", os.Symlink(fileB, fileA))

	for _, carrier := range makeCarriers(frozenA, false) {
		var release func()
		if carrier.install != nil {
			release = carrier.install()
		}
		for _, candidate := range []string{fileA, aliasLeaf, filepath.Join(aliasDir, "a.txt"), fileB} {
			if _, err := projectpaths.ResolveRead(t.Context(), nil, carrier.tc, candidate); err == nil {
				t.Fatalf("%s: ResolveRead(%q) succeeded after approved leaf was replaced by symlink to %q", carrier.name, candidate, fileB)
			}
		}
		if release != nil {
			release()
		}
	}
	for _, carrier := range makeCarriers(frozenA, true) {
		if carrier.name == "SessionReadPaths" {
			continue
		}
		var release func()
		if carrier.install != nil {
			release = carrier.install()
		}
		for _, candidate := range []string{fileA, aliasLeaf, filepath.Join(aliasDir, "a.txt"), fileB} {
			if _, err := projectpaths.ResolveWrite(t.Context(), nil, carrier.tc, candidate); err == nil {
				t.Fatalf("%s: ResolveWrite(%q) succeeded after approved leaf was replaced by symlink to %q", carrier.name, candidate, fileB)
			}
		}
		if release != nil {
			release()
		}
	}

	// 3. Approved-ancestor replacement: replace ancestor directory subC with a symlink to subD.
	subC := filepath.Join(outside, "sub-c")
	subD := filepath.Join(outside, "sub-d")
	testutil.FailErr(t, "mkdir sub-c", os.MkdirAll(subC, 0o700))
	testutil.FailErr(t, "mkdir sub-d", os.MkdirAll(subD, 0o700))
	fileC := filepath.Join(subC, "notes.txt")
	fileD := filepath.Join(subD, "notes.txt")
	testutil.FailErr(t, "write fileC", os.WriteFile(fileC, []byte("charlie\n"), 0o600))
	testutil.FailErr(t, "write fileD", os.WriteFile(fileD, []byte("delta\n"), 0o600))

	frozenC := grantedpath.Normalize(fileC)
	testutil.FailErr(t, "remove sub-c", os.RemoveAll(subC))
	testutil.FailErr(t, "replace sub-c with symlink to sub-d", os.Symlink(subD, subC))

	for _, carrier := range makeCarriers(frozenC, false) {
		var release func()
		if carrier.install != nil {
			release = carrier.install()
		}
		for _, candidate := range []string{fileC, fileD} {
			if _, err := projectpaths.ResolveRead(t.Context(), nil, carrier.tc, candidate); err == nil {
				t.Fatalf("%s: ResolveRead(%q) succeeded after approved ancestor was replaced by symlink to %q", carrier.name, candidate, subD)
			}
		}
		if release != nil {
			release()
		}
	}
	for _, carrier := range makeCarriers(frozenC, true) {
		if carrier.name == "SessionReadPaths" {
			continue
		}
		var release func()
		if carrier.install != nil {
			release = carrier.install()
		}
		for _, candidate := range []string{fileC, fileD} {
			if _, err := projectpaths.ResolveWrite(t.Context(), nil, carrier.tc, candidate); err == nil {
				t.Fatalf("%s: ResolveWrite(%q) succeeded after approved ancestor was replaced by symlink to %q", carrier.name, candidate, subD)
			}
		}
		if release != nil {
			release()
		}
	}
}
