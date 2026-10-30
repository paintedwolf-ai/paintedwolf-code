//go:build darwin || linux

package textutil

import (
	"bytes"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSpillFramesPreserveBytesAndReleaseWriteBuffer(t *testing.T) {
	spills := &Spills{}
	defer spills.Close()
	run, err := spills.Create(t.Context())
	testutil.FailErr(t, "create", err)
	records := [][]byte{{0, '\n', 0xff}, {}, []byte("last")}
	for _, record := range records {
		testutil.FailErr(t, "write", run.WriteRecord(record))
	}
	testutil.FailErr(t, "finish", run.Finish())
	if run.(*fileRun).writer != nil {
		t.Fatal("finished run retained its write buffer")
	}
	if err := run.WriteRecord([]byte("late")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write after finish: %v", err)
	}
	reader, err := run.Open(1024, func(int64) error { return nil })
	testutil.FailErr(t, "open", err)
	for _, want := range records {
		got, err := reader.Next()
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("record %q, want %q: %v", got, want, err)
		}
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("end: %v", err)
	}
	testutil.FailErr(t, "close reader", reader.Close())
	testutil.FailErr(t, "remove", run.Remove())
	if len(spills.runs) != 0 {
		t.Fatal("removed run retained")
	}
}

func TestSortMergeRefusalReleasesAllRuns(t *testing.T) {
	h := newHarness(t)
	h.owners.limits["sort_memory_bytes"] = 256
	h.owners.limits["sort_merge_fan_in"] = 2
	h.owners.limits["spill_bytes"] = 2600
	var input bytes.Buffer
	for range 100 {
		input.WriteString("abcdefghij\n")
	}
	h.run("sort", input.String()).expectRefusal(t, "intermediate merge limit", "COMMAND_LIMIT_EXCEEDED")
	if h.quota.reservations <= 25 {
		t.Fatal("test did not enter intermediate merges")
	}
	if h.quota.used != 0 || len(h.spills.runs) != 0 {
		t.Fatalf("refusal retained %d bytes and %d runs", h.quota.used, len(h.spills.runs))
	}
}

func TestSpillRecordLimitKeepsTypedCommandRefusal(t *testing.T) {
	run, err := (&Spills{}).Create(t.Context())
	testutil.FailErr(t, "create private run", err)
	t.Cleanup(func() { testutil.FailErr(t, "remove private run", run.Remove()) })
	testutil.FailErr(t, "write oversized frame", run.WriteRecord([]byte("ab")))
	testutil.FailErr(t, "finish run", run.Finish())
	reader, err := run.Open(1, func(int64) error { return nil })
	testutil.FailErr(t, "open bounded run", err)
	defer func() { testutil.FailErr(t, "close run", reader.Close()) }()
	cursor := mergeCursor{reader: reader}
	ok, err := cursor.advance()
	reject := toolrejection.AsToolReject(err)
	if ok || reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || reject.Data["limit_id"] != "record_bytes" || reject.Data["maximum"] != int64(1) || reject.Data["observed"] != int64(2) {
		t.Fatalf("spill frame lost its command refusal: ok=%v error=%v", ok, err)
	}
}
