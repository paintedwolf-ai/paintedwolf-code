package scratch

import (
	"bytes"
	"errors"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestByteSpoolSnapshotsAreAnonymousAndExact(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	spool, err := NewByteSpool()
	testutil.FailErr(t, "NewByteSpool failed", err)
	t.Cleanup(func() { _ = spool.Close() })
	content := []byte{'a', 0, 0xff, '\n'}
	if n, err := spool.Write(content); err != nil || n != len(content) {
		t.Fatalf("write %d: %v", n, err)
	}
	snapshot, err := spool.Reader()
	testutil.FailErr(t, "spool.Reader failed", err)
	if _, err := spool.Write([]byte("later")); err != nil {
		testutil.FailErr(t, "spool.Write failed", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("raw bytes have a pathname: %v %v", entries, err)
	}
	got, err := io.ReadAll(snapshot)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("snapshot %q: %v", got, err)
	}
	current, err := spool.Reader()
	testutil.FailErr(t, "spool.Reader failed", err)
	got, err = io.ReadAll(current)
	if err != nil || !bytes.Equal(got, append(content, []byte("later")...)) {
		t.Fatalf("current %q: %v", got, err)
	}
	if err := spool.Close(); err != nil {
		testutil.FailErr(t, "spool.Close failed", err)
	}
	if _, err := spool.Write(nil); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("write after close: %v", err)
	}
	if _, err := spool.Reader(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("reader after close: %v", err)
	}
	if _, err := snapshot.ReadAt(make([]byte, 1), 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("snapshot after close: %v", err)
	}
	if err := spool.Close(); err != nil {
		testutil.FailErr(t, "spool.Close failed", err)
	}
}

func TestByteSpoolSerializesConcurrentAppends(t *testing.T) {
	spool, err := NewByteSpool()
	testutil.FailErr(t, "NewByteSpool failed", err)
	defer spool.Close()
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 100 {
				if _, err := spool.Write([]byte("record\n")); err != nil {
					t.Error(err)
					return
				}
				reader, err := spool.Reader()
				if err != nil {
					t.Error(err)
					return
				}
				if reader.Size()%7 != 0 {
					t.Errorf("partial append visible: %d", reader.Size())
					return
				}
			}
		})
	}
	workers.Wait()
	reader, err := spool.Reader()
	testutil.FailErr(t, "spool.Reader failed", err)
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, bytes.Repeat([]byte("record\n"), 800)) {
		t.Fatalf("appends: %d bytes, %v", len(got), err)
	}
}
