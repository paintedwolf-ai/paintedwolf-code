package fseffect

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

var replaceStages = []struct {
	At        stage
	Committed bool
}{
	{stageCreated, false}, {stageWritten, false}, {stageModeSet, false},
	{stageFileSynced, false}, {stageFileClosed, false}, {stageValidated, false},
	{stageRenamed, true}, {stageDirSynced, true}, {stageVerified, true},
}

func TestReplaceFaultMatrixNeverLeavesPartialDestination(t *testing.T) {
	t.Parallel()
	for _, failure := range replaceStages {
		t.Run(string(failure.At), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			target := filepath.Join(dir, "document.txt")
			before, after := []byte("before\n"), []byte("after\r\n世界🐺")
			testutil.FailErr(t, "seed destination", os.WriteFile(target, before, 0o644))
			_, err := replace(ReplaceRequest{
				Location: PathLocation(target),
				Source:   bytes.NewReader(after),
				Mode:     0o644,
			}, func(at stage) error {
				if at == failure.At {
					return errors.New("fault")
				}
				return nil
			})
			if err == nil {
				t.Fatal("injected fault unexpectedly succeeded")
			}
			got, readErr := os.ReadFile(target)
			testutil.FailErr(t, "read destination after fault", readErr)
			want := before
			if failure.Committed {
				want = after
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("destination = %x, want complete bytes %x", got, want)
			}
			assertNoStagedSiblings(t, dir)
		})
	}
}

func TestReplaceDetectsPostcommitByteMismatch(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "document.txt")
	testutil.FailErr(t, "seed target", os.WriteFile(target, []byte("old\n"), 0o644))
	_, err := replace(ReplaceRequest{
		Location: PathLocation(target),
		Source:   bytes.NewReader([]byte("new\n")),
		Mode:     0o644,
	}, func(at stage) error {
		if at == stageDirSynced {
			return os.WriteFile(target, []byte("corrupt\n"), 0o644)
		}
		return nil
	})
	if !errors.Is(err, ErrPostcondition) {
		t.Fatalf("error = %v, want ErrPostcondition", err)
	}
}

func assertNoStagedSiblings(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	testutil.FailErr(t, "read staging directory", err)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") && strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("staged replacement leaked: %s", entry.Name())
		}
	}
}
