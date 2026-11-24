package mutation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestRedirectBindsOnlyWithAnEffectiveReviewGrant(t *testing.T) {
	h := newHarness(t, nil)
	shared, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve shared temp", err)
	t.Setenv("TMPDIR", shared)
	path := filepath.Join(shared, "output")
	declined := errors.New("declined")
	for _, outcome := range []string{"declined", "grant missing", "approved"} {
		t.Run(outcome, func(t *testing.T) {
			p := h.publication()
			p.ReviewRoot = func(_ context.Context, tc tools.ToolContext, operator, target, root string) (tools.ToolContext, error) {
				if operator != ">" || target != path || root != shared {
					t.Fatalf("reviewed operator %q, target %q, root %q", operator, target, root)
				}
				if outcome == "declined" {
					return tc, declined
				}
				if outcome == "approved" {
					tc.Files.GrantedWriteRoots = []string{root}
				}
				return tc, nil
			}
			bound, tc, err := p.bindOutput(t.Context(), h.destination(">", path))
			switch outcome {
			case "declined":
				if !errors.Is(err, declined) {
					t.Fatalf("declined redirect returned %v", err)
				}
			case "grant missing":
				if err == nil {
					t.Fatal("redirect bound without the installed root")
				}
			case "approved":
				testutil.FailErr(t, "bind reviewed redirect", err)
				if bound.Resolved.Abs != path || len(tc.Files.GrantedWriteRoots) != 1 || tc.Files.GrantedWriteRoots[0] != shared {
					t.Fatalf("bound path %q with grants %v", bound.Resolved.Abs, tc.Files.GrantedWriteRoots)
				}
			}
		})
	}
}

func TestRedirectWriteRootAsksForTheSmallestUsefulGrant(t *testing.T) {
	shared, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve shared temp", err)
	t.Setenv("TMPDIR", shared)
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "home directory", err)
	//nolint:usetesting // Must reside under the home directory, outside shared temp.
	outside, err := os.MkdirTemp(home, "paintedwolf-redirect-root-*")
	testutil.FailErr(t, "outside folder", err)
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	outside, err = filepath.EvalSymlinks(outside)
	testutil.FailErr(t, "resolve outside folder", err)
	file := filepath.Join(outside, "out.txt")
	for _, tc := range []struct{ name, path, requested, want string }{
		{"ordinary folder", file, "", outside},
		{"declared file", file, file, file},
		{"declared root refused", file, home, outside},
		{"declaration elsewhere", file, filepath.Join(shared, "x"), outside},
		{"shared temp", filepath.Join(shared, "out.txt"), "", confine.SharedTempLocation(filepath.Join(shared, "out.txt"))},
		{"declared temp file", filepath.Join(shared, "out.txt"), filepath.Join(shared, "out.txt"), filepath.Join(shared, "out.txt")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := redirectWriteRoot(tc.path, tc.requested)
			if !ok || got != tc.want {
				t.Fatalf("redirectWriteRoot(%q, %q) = %q, %v; want %q", tc.path, tc.requested, got, ok, tc.want)
			}
		})
	}
}
