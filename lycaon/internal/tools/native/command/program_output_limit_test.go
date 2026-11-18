package command

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRetentionQuotaKeepsTypedScreenedPrefix(t *testing.T) {
	runtime := &ProgramRuntime{Recorder: invocation.NewSQLRecorder(testdbfixture.Open(t, "output.db")), Blobs: blobstore.Store{Root: t.TempDir()}, Projector: captureprojection.New(secretmatch.NewInertMatcher(), nil)}
	account := runtime.sessionResources("session")
	account.limit = 4 << 20
	stream := newProgramStream(t.Context(), 256<<20, account, nil)
	defer stream.Close()
	body := bytes.Repeat([]byte("screened line\n"), 100000)
	_, err := stream.Write(body)
	testutil.FailErr(t, "capture source stream", err)
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 32 << 20, State: 64 << 20})
	testutil.FailErr(t, "program memory", err)
	run := &programRun{memory: memory}
	run.root.Identity.ProgramID = "program"
	spill, _, err := runtime.retainProgramStream(t.Context(), run, stream, "stdout", captureprojection.Scope{SessionID: "session"})
	testutil.FailErr(t, "retain screened prefix", err)
	defer spill.Close()
	limit := outputDeliveryLimit(spill.Failure)
	if spill.Complete || limit == nil || limit.LimitID != "session_spill_bytes" || limit.Maximum != 4<<20 || limit.Observed <= limit.Maximum {
		t.Fatalf("spill=%+v limit=%+v", spill, limit)
	}
	reader, err := spill.Open(t.Context())
	testutil.FailErr(t, "open prefix", err)
	prefix, err := io.ReadAll(reader)
	testutil.FailErr(t, "read prefix", err)
	testutil.FailErr(t, "close prefix", reader.Close())
	if len(prefix) == 0 || len(prefix) >= len(body) || !bytes.Equal(prefix, body[:len(prefix)]) {
		t.Fatalf("prefix=%d input=%d", len(prefix), len(body))
	}
	testutil.FailErr(t, "release retained prefix", spill.Close())
	testutil.FailErr(t, "release raw stage", stream.Close())
	if account.spill != 0 {
		t.Fatalf("spill quota leaked %d", account.spill)
	}
}

func TestRejectOutputScreenLimitPreservesExitAndAvailableOutput(t *testing.T) {
	exit := 0
	paths := []api.CommandProgramOutputWindow{{OutputPath: "@output/program.stdout"}, {}}
	for _, cause := range []error{
		&captureprojection.LimitError{Resource: "screen_work_units", Maximum: 8, Observed: 9},
		&secretmatch.StreamLimitError{Resource: "screen_work_units", Maximum: 8, Observed: 9},
	} {
		t.Run(fmt.Sprintf("%T", cause), func(t *testing.T) {
			reject := rejectOutputScreenLimit(fmt.Errorf("deliver output: %w", cause), &exit, paths)
			if reject == nil || reject.Code != "COMMAND_OUTPUT_SCREEN_LIMIT" {
				t.Fatalf("limit classified as %+v", reject)
			}
			details := reject.Data
			if details["resource"] != "screen_work_units" || details["maximum"] != int64(8) || details["observed"] != int64(9) ||
				details["command_exit_recorded"] != true || details["command_exit_status"] != 0 ||
				!slices.Equal(details["saved_output_paths"].([]string), []string{"@output/program.stdout"}) {
				t.Fatalf("limit facts = %+v", details)
			}
			reject = rejectOutputScreenLimit(cause, nil, nil)
			if reject.Data["command_exit_recorded"] != nil || reject.Data["saved_output_paths"] != nil {
				t.Fatalf("unobserved exit or path reported: %+v", reject.Data)
			}
		})
	}
	if reject := rejectOutputScreenLimit(errors.New("other fault"), nil, nil); reject != nil {
		t.Fatalf("non-limit classified as %+v", reject)
	}
}
