package mutation

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

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
