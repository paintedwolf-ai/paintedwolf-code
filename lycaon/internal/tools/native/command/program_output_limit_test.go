package command

import (
	"bytes"
	"io"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
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
