package debugretention

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCappedFileStartsFreshSegmentAtLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	file, err := OpenFile(path, 12)
	testutil.FailErr(t, "open", err)
	testutil.FailErr(t, "write first", writeAll(file, []byte("first\n")))
	testutil.FailErr(t, "write second", writeAll(file, []byte("second\n")))
	testutil.FailErr(t, "close", file.Close())
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read", err)
	if string(raw) != "second\n" {
		t.Fatalf("capture = %q, want fresh segment", raw)
	}
}

func writeAll(file *File, data []byte) error {
	_, err := file.Write(data)
	return err
}
