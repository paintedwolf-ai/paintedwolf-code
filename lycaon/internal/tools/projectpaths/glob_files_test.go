//go:build darwin || linux

package projectpaths_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestGlobFilesMatchConfinedLogicalPaths(t *testing.T) {
	tc, root := readSessionContext(t)
	outside := t.TempDir()
	for _, file := range []string{filepath.Join(root, ".hidden.go"), filepath.Join(root, "pkg", "deep", "c.go"), filepath.Join(outside, "o.go")} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(file), 0o755))
		testutil.FailErr(t, "write", os.WriteFile(file, nil, 0o600))
	}
	testutil.FailErr(t, "link inside", os.Symlink("pkg", filepath.Join(root, "inner")))
	testutil.FailErr(t, "link outside", os.Symlink(outside, filepath.Join(root, "escape")))
	paths, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "program paths", err)
	files := projectpaths.GlobFiles{Paths: paths}
	cases := []struct {
		pattern string
		want    []string
	}{
		{"*.go", []string{"a.go"}},
		{".*.go", []string{".hidden.go"}},
		{"*", []string{"a.go", "escape", "inner", "pkg"}},
		{"*/", []string{"pkg/"}},
		{"pkg/*.go", []string{"pkg/b.go"}},
		{"**/*.go", []string{"a.go", "pkg/b.go", "pkg/deep/c.go"}},
		{"inner/*.go", nil},
		{"escape/*", nil},
		{"@ws/*.go", nil},
		{"./p*/b.go", []string{"./pkg/b.go"}},
		{filepath.ToSlash(root) + "/*.go", []string{filepath.ToSlash(root) + "/a.go"}},
		// The walk applies exactly the invocation's read authority, which
		// admits ordinary host reads when no boundary narrows it.
		{filepath.ToSlash(outside) + "/*.go", []string{filepath.ToSlash(outside) + "/o.go"}},
		{"@scratch/*", nil},
		{"missing/*.go", nil},
	}
	for _, c := range cases {
		t.Run(c.pattern, func(t *testing.T) {
			budget := commandsurface.GlobBudget{Matches: 100, Visits: 100}
			got, err := commandsurface.MatchGlob(t.Context(), files, c.pattern, &budget)
			testutil.FailErr(t, "match "+c.pattern, err)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("%s matched %q, want %q", c.pattern, got, c.want)
			}
		})
	}
}

func TestGlobFilesReachRootsThroughLinkedParents(t *testing.T) {
	target := t.TempDir()
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(target, "ws", "pkg"), 0o755))
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(target, "ws", "a.go"), nil, 0o600))
	linked := filepath.Join(t.TempDir(), "linked")
	testutil.FailErr(t, "link parent", os.Symlink(target, linked))
	testutil.FailErr(t, "link below root", os.Symlink("..", filepath.Join(target, "ws", "up")))
	root := filepath.Join(linked, "ws")
	tc := tools.ToolContext{
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "ws", Path: root, IsPrimary: true}}, ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID},
	}
	paths, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "program paths", err)
	files := projectpaths.GlobFiles{Paths: paths}
	spelled := filepath.ToSlash(root)
	canonical := filepath.ToSlash(fspath.CanonicalPath(root))
	for pattern, want := range map[string][]string{
		spelled + "/*.go":    {spelled + "/a.go"},
		canonical + "/*.go":  {canonical + "/a.go"},
		spelled + "/*/":      {spelled + "/pkg/"},
		spelled + "/up/ws/*": nil,
		spelled + "/up/*":    nil,
		"@ws/up/ws/a.go*":    nil,
	} {
		budget := commandsurface.GlobBudget{Matches: 100, Visits: 100}
		got, err := commandsurface.MatchGlob(t.Context(), files, pattern, &budget)
		testutil.FailErr(t, "match "+pattern, err)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s matched %q, want %q", pattern, got, want)
		}
	}
}

func TestGlobFilesKeepCancellationAndLimits(t *testing.T) {
	tc, _ := readSessionContext(t)
	paths, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "program paths", err)
	files := projectpaths.GlobFiles{Paths: paths}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for name, probe := range map[string]func() error{
		"list":  func() error { _, err := files.ListDir(ctx, "", 10); return err },
		"entry": func() error { _, _, err := files.Entry(ctx, "a.go"); return err },
		"admit": func() error { _, err := files.Admit(ctx, "a.go"); return err },
	} {
		if err := probe(); !errors.Is(err, context.Canceled) {
			t.Errorf("%s under a canceled context: %v", name, err)
		}
	}
	operand := filepath.Join("pkg", strings.Repeat("x", 1<<18))
	var pathErr *projectpaths.PathError
	if _, _, err := files.Entry(t.Context(), operand); !errors.As(err, &pathErr) || pathErr.LimitID != "argument_bytes" {
		t.Fatalf("oversized literal component: %v", err)
	}
	if entry, found, err := files.Entry(t.Context(), "/dev/null"); err != nil || found || entry.Dir {
		t.Fatalf("null device entry = %+v %v %v", entry, found, err)
	}
	if ok, err := files.Admit(t.Context(), "/dev/null"); err != nil || ok {
		t.Fatalf("null device admitted = %v %v", ok, err)
	}
	if entries, err := files.ListDir(t.Context(), "a.go", 10); err != nil || len(entries) != 0 {
		t.Fatalf("file listed as a directory: %v %v", entries, err)
	}
}
