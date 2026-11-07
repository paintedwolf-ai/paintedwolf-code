package modelstore

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPublicationFlushesCompleteDirectoryAncestry(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "models", "revisions", "e4b", "vulkan")
	testutil.FailErr(t, "create revision directories", os.MkdirAll(target, 0o700))
	root, err := os.OpenRoot(filepath.Join(base, "models"))
	testutil.FailErr(t, "open store", err)
	defer func() { testutil.FailErr(t, "close publication root", root.Close()) }()
	physical, err := filepath.EvalSymlinks(target)
	testutil.FailErr(t, "resolve revision directory", err)
	var want []string
	for path := physical; ; path = filepath.Dir(path) {
		want = append(want, path)
		if filepath.Dir(path) == path {
			break
		}
	}
	var got []string
	flush := func(directory *os.Root, name string) error {
		path, err := filepath.EvalSymlinks(filepath.Join(directory.Name(), name))
		if err != nil {
			return err
		}
		got = append(got, path)
		return fseffect.SyncDirectory(directory, name)
	}
	testutil.FailErr(t, "flush publication", syncPublicationDirectories(root, "revisions/e4b/vulkan", flush))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("directory flush order: got %v, want %v", got, want)
	}
	for failAt := range len(want) {
		t.Run(fmt.Sprintf("flush_failure_%d", failAt), func(t *testing.T) {
			failure := errors.New("directory storage failure")
			calls := 0
			err := syncPublicationDirectories(root, "revisions/e4b/vulkan", func(*os.Root, string) error {
				calls++
				if calls == failAt+1 {
					return failure
				}
				return nil
			})
			if !errors.Is(err, failure) || calls != failAt+1 {
				t.Fatalf("flush failure: err=%v calls=%d", err, calls)
			}
		})
	}
}

type interruptedPayload struct{ delivered bool }

func (r *interruptedPayload) Read(p []byte) (int, error) {
	if r.delivered {
		return 0, io.ErrUnexpectedEOF
	}
	r.delivered = true
	return copy(p, "partial"), nil
}

func TestInterruptedPayloadPreservesActiveRevision(t *testing.T) {
	s := newStore(t)
	working := publish(t, s, "working", "working")
	replacement := testArtifact(t, []byte("partial replacement"))
	if err := s.Stage(&interruptedPayload{}, replacement); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("interrupted payload: %v", err)
	}
	if s.Verified(replacement.Digest) {
		t.Fatal("interrupted payload was published")
	}
	entries, err := filepath.Glob(filepath.Join(s.root, "artifacts", replacement.Digest[:2], ".staging-*"))
	testutil.FailErr(t, "inspect interrupted staging", err)
	if len(entries) != 0 {
		t.Fatalf("interrupted staging remains: %v", entries)
	}
	reopened := New(s.root, acceptSigner{})
	state := reopened.Status(working.Model, working.Device)
	if state.State != StateReady || state.Active == nil || state.Active.Revision != working.Revision {
		t.Fatalf("working revision unavailable after interrupted payload: %+v", state)
	}
}
