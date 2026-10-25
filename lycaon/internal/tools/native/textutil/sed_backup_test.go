package textutil

import (
	"context"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestSedBackupNonseekableReplayAdmission(t *testing.T) {
	for _, maximum := range []int64{64, 8} {
		t.Run(strconv.FormatInt(maximum, 10), func(t *testing.T) {
			h := newHarness(t)
			h.seed("original", "original\n")
			h.owners.limits["spill_bytes"] = maximum
			request, release := h.bind("sed -i.bak p original")
			defer release()
			prepared, err := h.owners.Prepare(t.Context(), request)
			testutil.FailErr(t, "prepare real command", err)
			inv := newInvocation(t.Context(), prepared, h.ports(), commandprogram.Streams{Output: io.Discard})
			defer inv.close()
			run := sedRun{inv: inv}
			text := "first\x00\xff\nsecond\n"
			backup, err := run.prepareBackup(prepared.Argv.Operands()[0], io.NopCloser(strings.NewReader(text)), ".bak")
			if backup == nil || backup.stage == nil || h.quota.used != maximum {
				t.Fatalf("backup=%+v quota=%d err=%v", backup, h.quota.used, err)
			}
			if maximum >= int64(len(text)) {
				testutil.FailErr(t, "stage nonseekable backup", err)
				data, readErr := io.ReadAll(backup.replay)
				testutil.FailErr(t, "read bounded fixture replay", readErr)
				if string(data) != text {
					t.Fatalf("replayed %q", data)
				}
			} else {
				limit := toolrejection.AsToolReject(err)
				if limit == nil || limit.Code != "COMMAND_LIMIT_EXCEEDED" || limit.Data["limit_id"] != "spill_bytes" || limit.Data["maximum"] != maximum || limit.Data["observed"] != int64(len(text)) {
					t.Fatalf("spill refusal %v", err)
				}
			}
			testutil.FailErr(t, "abandon unpublished backup", backup.stage.Abandon(context.WithoutCancel(t.Context())))
			testutil.FailErr(t, "close backup", backup.Close())
			testutil.FailErr(t, "close backup again", backup.Close())
			inv.close()
			if h.quota.used != 0 || h.memory.InUse().Working != 0 {
				t.Fatalf("retained quota=%d memory=%+v", h.quota.used, h.memory.InUse())
			}
		})
	}
}
