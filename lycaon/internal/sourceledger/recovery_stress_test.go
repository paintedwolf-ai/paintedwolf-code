//go:build stress

package sourceledger

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStressRecoveryManifestLargeTree(t *testing.T) {
	store, ctx := openLedger(t)
	root, err := os.OpenRoot(t.TempDir())
	testutil.FailErr(t, "open root", err)
	defer func() { _ = root.Close() }()
	writer, err := store.Retention.BeginRecovery(ctx, "p1", "large-tree")
	testutil.FailErr(t, "begin recovery", err)
	defer writer.Close()
	const files = 15000
	body := bytes.Repeat([]byte("recovery bytes\n"), 1000)
	for i := 0; i < files; i++ {
		name := fmt.Sprintf("%05d", i)
		content := append([]byte(name+"\n"), body...)
		testutil.FailErr(t, "seed file", root.WriteFile(name, content, 0o600))
		_, err := writer.Append(ctx, RecoveryEntry{Path: name, Mode: 0o600}, root, name, nil)
		testutil.FailErr(t, "capture entry", err)
	}
	testutil.FailErr(t, "flush manifest", writer.Flush(ctx))
	count := 0
	testutil.FailErr(t, "stream manifest", store.Retention.WalkRecovery(ctx, "p1", "large-tree", files, false, func(entry RecoveryEntry) error {
		count++
		return store.Retention.CopyRecoveryFile(ctx, entry.SHA, io.Discard)
	}))
	if count != files {
		t.Fatalf("recovered entries=%d want=%d", count, files)
	}
	t.Logf("verified %d files and %d logical bytes", files, files*(len(body)+6))
}

func TestStressRecoveryManifestLargeReverseTraversal(t *testing.T) {
	store, ctx := openLedger(t)
	writer, err := store.Retention.BeginRecovery(ctx, "p1", "large-manifest")
	testutil.FailErr(t, "begin recovery", err)
	defer writer.Close()
	const count = 100001
	for i := 0; i < count; i++ {
		_, err := writer.Append(ctx, RecoveryEntry{Path: fmt.Sprintf("%06d", i), Mode: uint32(os.ModeDir | 0o700)}, nil, "", nil)
		testutil.FailErr(t, "append directory", err)
	}
	testutil.FailErr(t, "flush manifest", writer.Flush(ctx))
	read := 0
	testutil.FailErr(t, "read reverse manifest", store.Retention.WalkRecovery(ctx, "p1", "large-manifest", count, true, func(entry RecoveryEntry) error {
		if entry.Path != fmt.Sprintf("%06d", count-1-read) {
			return fmt.Errorf("manifest order at %d: %s", read, entry.Path)
		}
		read++
		return nil
	}))
	if read != count {
		t.Fatalf("manifest entries=%d", read)
	}
}

type recoveryByteCounter int64

func (count *recoveryByteCounter) Write(data []byte) (int, error) {
	*count += recoveryByteCounter(len(data))
	return len(data), nil
}

func TestStressRecoverySingleLargeFile(t *testing.T) {
	store, ctx := openLedger(t)
	root, err := os.OpenRoot(t.TempDir())
	testutil.FailErr(t, "open source root", err)
	defer func() { _ = root.Close() }()
	file, err := root.OpenFile("large", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	testutil.FailErr(t, "create sparse source", err)
	const size = 128 << 20
	testutil.FailErr(t, "size source beyond decoder memory budget", file.Truncate(size))
	testutil.FailErr(t, "close source", file.Close())
	writer, err := store.Retention.BeginRecovery(ctx, "p1", "large-file")
	testutil.FailErr(t, "begin recovery", err)
	defer writer.Close()
	saved, err := writer.Append(ctx, RecoveryEntry{Path: ".", Mode: 0o600}, root, "large", nil)
	testutil.FailErr(t, "retain large file", err)
	testutil.FailErr(t, "flush recovery", writer.Flush(ctx))
	writer.Close()
	var restored recoveryByteCounter
	testutil.FailErr(t, "stream large recovery", store.Retention.CopyRecoveryFile(ctx, saved.SHA, &restored))
	if int64(restored) != size {
		t.Fatalf("restored bytes = %d, want %d", restored, size)
	}
}
