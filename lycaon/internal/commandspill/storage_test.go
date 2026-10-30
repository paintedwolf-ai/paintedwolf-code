package commandspill

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRunFramingAndRecordAdmission(t *testing.T) {
	run, err := (DirectorySpills{Dir: t.TempDir()}).Create(t.Context())
	testutil.FailErr(t, "create private run", err)
	t.Cleanup(func() { testutil.FailErr(t, "remove private run", run.Remove()) })
	testutil.FailErr(t, "write framed record", run.WriteRecord([]byte("a\x00b\n")))
	testutil.FailErr(t, "finish private run", run.Finish())
	var growth []int64
	reader, err := run.Open(3, func(n int64) error { growth = append(growth, n); return nil })
	testutil.FailErr(t, "open bounded reader", err)
	_, err = reader.Next()
	var limit *RecordLimitError
	if !errors.As(err, &limit) || limit.Maximum != 3 || limit.Observed != 4 || len(growth) != 1 {
		t.Fatalf("oversized frame was not refused before record growth: error=%v growth=%v", err, growth)
	}
	testutil.FailErr(t, "close bounded reader", reader.Close())
	reader, err = run.Open(4, func(int64) error { return nil })
	testutil.FailErr(t, "open admitted reader", err)
	defer func() { testutil.FailErr(t, "close admitted reader", reader.Close()) }()
	record, err := reader.Next()
	testutil.FailErr(t, "read exact framed bytes", err)
	if string(record) != "a\x00b\n" {
		t.Fatalf("framing changed record: %q", record)
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected terminal EOF, got %v", err)
	}
}

func TestRunFrameLengthCannotWrapOrAllocateBeforeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name              string
		size              uint64
		maximum, observed int64
	}{
		{"unsigned overflow", math.MaxUint64, 3, 1 << 62},
		{"signed boundary", math.MaxInt64, 3, 1 << 62},
		{"negative maximum", 0, -1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var frame [binary.MaxVarintLen64]byte
			n := binary.PutUvarint(frame[:], tc.size)
			path := filepath.Join(t.TempDir(), "oversized-run")
			testutil.FailErr(t, "seed oversized frame", os.WriteFile(path, frame[:n], 0600))
			var growth []int64
			run := &fileRun{path: path}
			reader, err := run.Open(tc.maximum, func(n int64) error { growth = append(growth, n); return nil })
			testutil.FailErr(t, "open framed input", err)
			defer func() { testutil.FailErr(t, "close framed input", reader.Close()) }()
			_, err = reader.Next()
			var limit *RecordLimitError
			if !errors.As(err, &limit) || limit.Maximum != tc.maximum || limit.Observed != tc.observed || len(growth) != 1 || growth[0] != 64<<10 {
				t.Fatalf("unsafe record frame reached allocation or lost its limit: error=%v growth=%v", err, growth)
			}
		})
	}
}
