package command

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandIORedirectUsesReviewedWriteRoots(t *testing.T) {
	// A nonexistent root exercises resolution without external filesystem writes.
	root := filepath.Join(filepath.VolumeName(os.TempDir())+string(filepath.Separator), "approved-command-output", t.Name())
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{
		{ID: "implement", Tools: map[string]bool{"write": true, "command": true, "verify": true}},
	})
	for _, tool := range []string{"command", "verify"} {
		for _, field := range []string{"stdout_to", "stderr_to"} {
			t.Run(tool+"/"+field, func(t *testing.T) {
				tc := testToolContext(t.TempDir())
				path := filepath.Join(root, "output.txt")
				args := map[string]any{field: path}
				if _, err := commandIOFor(t.Context(), boundary, tc, args, tool); err == nil {
					t.Fatal("unapproved external redirect was accepted")
				}
				tc.GrantedWriteRoots = []string{root}
				params, err := commandIOFor(t.Context(), boundary, tc, args, tool)
				testutil.FailErr(t, "resolve redirect inside reviewed write root", err)
				if params.Redirect == nil {
					t.Fatal("approved redirect was not resolved")
				}
				if len(tc.Roots) != 1 || tc.Roots[0].ID != "primary" {
					t.Fatal("write grant changed attached project roots")
				}
				for _, rejected := range []string{
					filepath.Join(root+"-sibling", "output.txt"),
					filepath.Join(root, ".git", "config"),
				} {
					if _, err := commandIOFor(t.Context(), boundary, tc, map[string]any{field: rejected}, tool); err == nil {
						t.Fatalf("write-root grant covered refused redirect %q", rejected)
					}
				}
				_, err = commandIOFor(t.Context(), boundary, tc, map[string]any{field: filepath.Join(root, ".paintedwolf", "approvals.yaml")}, tool)
				testutil.FailErr(t, "resolve policy output for commit-time diff review", err)
				tc.GrantedWriteRoots = nil
				if _, err := commandIOFor(t.Context(), boundary, tc, args, tool); err == nil {
					t.Fatal("redirect retained authority after grant removal")
				}
			})
		}
	}
}
