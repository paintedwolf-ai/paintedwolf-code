package textutil

import (
	"testing"

	"github.com/lycaon/lycaon/internal/commandspill"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestSpillRecordLimitKeepsTypedCommandRefusal(t *testing.T) {
	run, err := (commandspill.DirectorySpills{Dir: t.TempDir()}).Create(t.Context())
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
