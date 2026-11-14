package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Removing a source root must not turn unavailable content into an empty tree.
// This exercises path disappearance, not a network transport interruption.
func TestSourceRootDisappearancePreservesCheckpointAndRetry(t *testing.T) {
	for _, afterPreview := range []bool{false, true} {
		t.Run(fmt.Sprintf("after-preview-%v", afterPreview), func(t *testing.T) {
			parent := t.TempDir()
			source, detached := filepath.Join(parent, "source"), filepath.Join(parent, "detached")
			testutil.FailErr(t, "create source", os.Mkdir(source, 0755))
			for i := range 3 {
				testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(source, fmt.Sprintf("f%02d.txt", i)), []byte("base\n"), 0640))
			}
			store, promoter, preview := publishManyChanges(t, source, 3)
			before, err := store.LoadManifest("cp-scale")
			testutil.FailErr(t, "read published checkpoint", err)
			testutil.FailErr(t, "detach only fixture source", os.Rename(source, detached))
			if afterPreview {
				_, err = promoteAndCommit(t, promoter, ApplyRequest{Preview: preview})
			} else {
				_, err = promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "cp-scale"})
			}
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unavailable root must fail explicitly, got %v", err)
			}
			if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed operation recreated source root: %v", err)
			}
			after, err := store.LoadManifest("cp-scale")
			testutil.FailErr(t, "reopen checkpoint during unavailability", err)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed operation changed published checkpoint")
			}
			for i := range 3 {
				body, err := os.ReadFile(filepath.Join(detached, fmt.Sprintf("f%02d.txt", i)))
				testutil.FailErr(t, "read detached source", err)
				if string(body) != "base\n" {
					t.Fatalf("failed operation changed source bytes: %q", body)
				}
			}
			testutil.FailErr(t, "restore fixture source", os.Rename(detached, source))
			preview, err = promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "cp-scale"})
			testutil.FailErr(t, "renew preview after source restoration", err)
			_, err = promoteAndCommit(t, promoter, ApplyRequest{Preview: preview})
			testutil.FailErr(t, "retry retained checkpoint", err)
			for i := range 3 {
				body, err := os.ReadFile(filepath.Join(source, fmt.Sprintf("f%02d.txt", i)))
				testutil.FailErr(t, "read promoted source", err)
				if string(body) != "worker\n" {
					t.Fatalf("retry lost checkpoint bytes: %q", body)
				}
			}
		})
	}
}
