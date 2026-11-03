package projectsource

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTreeAllocationChargesDirectoriesAndHardLinkedStorageOnce(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "first")
	testutil.FailErr(t, "seed payload", os.WriteFile(file, []byte("payload"), 0o600))
	testutil.FailErr(t, "link payload", os.Link(file, filepath.Join(root, "second")))
	testutil.FailErr(t, "seed empty directory", os.Mkdir(filepath.Join(root, "empty"), 0o750))
	var expected int64
	for _, path := range []string{root, file, filepath.Join(root, "empty")} {
		info, err := os.Lstat(path)
		testutil.FailErr(t, "read allocation metadata", err)
		facts := fspath.EntryAllocation(info)
		if !facts.Known {
			t.Fatal("allocation facts unavailable on the test platform")
		}
		expected += facts.Bytes
	}
	_, size, err := sourceTreeFingerprint(t.Context(), root, testSourceOperationMemory(t))
	testutil.FailErr(t, "measure tree", err)
	if !size.AllocationKnown || size.AllocatedBytes != expected || size.Bytes != 2*int64(len("payload")) || size.Files != 2 {
		t.Fatalf("measurement=%+v expected allocation=%d", size, expected)
	}
}

type unobservedAllocationInfo struct{ fs.FileInfo }

func (unobservedAllocationInfo) Sys() any { return nil }

func TestAllocationUnavailableAndCancellationDoNotEstimateOrLeak(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	testutil.FailErr(t, "seed payload", os.WriteFile(file, []byte("payload"), 0o600))
	testutil.FailErr(t, "link payload", os.Link(file, filepath.Join(root, "link")))
	info, err := os.Lstat(file)
	testutil.FailErr(t, "observe file", err)
	unknown := &sourceTreeAllocation{known: true, memory: testSourceOperationMemory(t)}
	testutil.FailErr(t, "observe missing storage metadata", unknown.observe(t.Context(), unobservedAllocationInfo{info}))
	if unknown.known || unknown.bytes != 0 || unknown.db != nil {
		t.Fatalf("guessed allocation: %+v", unknown)
	}
	measure := &sourceTreeAllocation{known: true, memory: testSourceOperationMemory(t)}
	testutil.FailErr(t, "open exact link set", measure.observe(t.Context(), info))
	if measure.db == nil {
		t.Fatal("linked-entry set was not opened")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := measure.observe(ctx, info); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	testutil.FailErr(t, "close temporary set", measure.close())
	if measure.db != nil || measure.tx != nil {
		t.Fatal("measurement retained temporary handles")
	}
}
