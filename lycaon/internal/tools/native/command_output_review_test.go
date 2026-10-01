package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestCommandOutputReviewsActualBytesBeforeReplacingInstructions(t *testing.T) {
	for _, rel := range []string{"AGENTS.md", ".paintedwolf/approvals.yaml"} {
		t.Run(rel, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, rel)
			testutil.FailErr(t, "seed policy directory", os.MkdirAll(filepath.Dir(path), 0o755))
			testutil.FailErr(t, "seed instructions", os.WriteFile(path, []byte("before\n"), 0o644))
			tc := testToolContext(root)
			refused := errors.New("user declined change")
			reviewed := false
			tc.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
				reviewed = true
				current, err := os.ReadFile(path)
				testutil.FailErr(t, "read while output awaits review", err)
				if string(current) != "before\n" {
					t.Fatal("redirect changed destination before approval")
				}
				if len(changes) != 1 || changes[0].Preview.After != "before\nafter\n" {
					t.Fatalf("incorrect appended preview: %+v", changes)
				}
				return refused
			}
			params, err := commandIOFor(t.Context(), nativefixture.Boundary(t), tc, map[string]any{"stdout_to": rel, "append": true}, "command")
			testutil.FailErr(t, "prepare command output", err)
			_, err = exec.RunPipeline(t.Context(), []exec.Stage{{Name: "printf", Args: []string{"after\n"}}}, exec.ExecOpts{
				Launch: exec.HostLaunch("redirect approval fixture"), Dir: root, Redirect: params.Redirect,
			})
			if !errors.Is(err, refused) || !reviewed {
				t.Fatalf("redirect approval err=%v reviewed=%v", err, reviewed)
			}
			current, err := os.ReadFile(path)
			testutil.FailErr(t, "read declined output destination", err)
			if string(current) != "before\n" {
				t.Fatal("declined output changed instructions")
			}
		})
	}
}
