package fssync_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFileFlushesAndReportsClosedFiles(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "f"))
	testutil.FailErr(t, "create file", err)
	_, err = f.WriteString("durable content")
	testutil.FailErr(t, "write file", err)
	testutil.FailErr(t, "flush open file", fssync.File(f))
	testutil.FailErr(t, "close file", f.Close())
	if err := fssync.File(f); err == nil {
		t.Fatal("flushing a closed file must fail")
	}
}
