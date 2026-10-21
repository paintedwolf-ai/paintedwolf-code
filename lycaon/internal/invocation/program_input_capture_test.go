package invocation_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type inputCaptureQuota struct {
	outputQuota
	detector int64
}

func (q *inputCaptureQuota) ReserveBytes(_ context.Context, n int64) error {
	q.detector += n
	return nil
}
func (q *inputCaptureQuota) ReleaseBytes(n int64) { q.detector -= n }

func TestProgramReviewedInputCaptureScreensOriginalBytesAndRetainsIndependentProgress(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	quota := &inputCaptureQuota{}
	outputs, err := invocation.NewProgramOutputRetention(t.Context(), f.recorder, blobstore.Store{Root: t.TempDir()}, quota)
	testutil.FailErr(t, "admit real invocation output lifetime", err)
	testutil.FailErr(t, "reconstruct prior durable outputs", outputs.Restore(t.Context()))
	const protected = "fixture-protected-value"
	matcher := secretmatch.NewInertMatcher()
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{Name: "token", Secret: protected, RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch, NonDisclosable: true}}
	})
	capture := invocation.ProgramInputCapture{Outputs: outputs, Projector: captureprojection.New(matcher, nil), ProgramID: f.program, Scope: captureprojection.Scope{RootSessionID: f.session, SessionID: f.session}, Screening: captureprojection.StreamOptions{WorkingMemory: quota, Bounds: secretmatch.StreamBounds{InputBytes: 1 << 20, OutputBytes: 1 << 20, MemoryBytes: 32 << 20, DetectorWork: 1 << 30}}, MaximumNodes: 1024, MaximumDepth: 64}
	original := map[string]any{"path": "raw\xff/" + protected, "count": json.Number("9007199254740993")}
	reviewed, releaseReview, err := toolcontract.NewReviewedOperationInputs(t.Context(), quota, original)
	testutil.FailErr(t, "freeze real lossless opaque-reference input", err)
	recorder := invocation.ProgramAdmissionRecorder{Recorder: f.recorder, Inputs: capture}
	testutil.FailErr(t, "screen retain and durably mark actual native child", recorder.BeforeNativeOwner(t.Context(), child, "actual-model-call", reviewed))
	var reference, digest, representation string
	testutil.FailErr(t, "read actual reviewed provenance", f.database.QueryRowContext(t.Context(), `SELECT reviewed_arguments_ref,reviewed_arguments_digest,reviewed_arguments_representation FROM invocation_program_executions WHERE id=?`, child.Invocation().Identity.ExecutionID).Scan(&reference, &digest, &representation))
	if digest != reviewed.ArgumentsDigest() || representation != toolcontract.LosslessOperationArgumentsV1 {
		t.Fatal("screened inspection replaced actual reviewed identity")
	}
	reader, err := outputs.Open(t.Context(), f.session, f.program, reference)
	testutil.FailErr(t, "fetch actual retained inspection body", err)
	body, err := io.ReadAll(reader)
	testutil.FailErr(t, "read bounded fixture inspection body", err)
	testutil.FailErr(t, "close inspection reader", reader.Close())
	sum := sha256.Sum256(body)
	inspection, releaseInspection, err := toolcontract.RestoreReviewedOperationInputs(t.Context(), quota, representation, hex.EncodeToString(sum[:]), string(body))
	testutil.FailErr(t, "restore explicit screened inspection representation", err)
	arguments, releaseArguments, err := inspection.Arguments(t.Context(), quota)
	testutil.FailErr(t, "decode screened original-byte scalar representation", err)
	value := arguments["path"].(string)
	if !strings.Contains(value, "\xff") || strings.Contains(value, protected) || arguments["count"] != json.Number("9007199254740993") {
		t.Fatalf("lossy or unscreened inspection: %#v", arguments)
	}
	if original["path"] != "raw\xff/"+protected {
		t.Fatal("inspection mutated live owner input")
	}
	releaseArguments()
	releaseInspection()
	releaseReview()
	progressIdentity := toolcontract.ProgramProgressIdentity{ProgramID: f.program, ExecutionID: uuid.NewString(), ActorID: "fixture-person", SessionID: f.session, RootSessionID: f.session, Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}}
	progressReview, releaseProgress, err := toolcontract.NewReviewedOperationInputs(t.Context(), quota, map[string]any{"content": "- [ ] fixture"})
	testutil.FailErr(t, "freeze actual envelope review", err)
	testutil.FailErr(t, "retain and mark independent progress envelope", recorder.BeforeProgressOwner(t.Context(), progressIdentity, "actual-model-call", progressReview))
	releaseProgress()
	var envelopeInputs, children, observations int
	testutil.FailErr(t, "inspect envelope input without fake child", f.database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_outputs WHERE program_id=? AND execution_id IS NULL AND initiating_claim_id=?`, f.program, progressIdentity.ExecutionID).Scan(&envelopeInputs))
	testutil.FailErr(t, "inspect actual dynamic child count", f.database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_executions WHERE program_id=?`, f.program).Scan(&children))
	testutil.FailErr(t, "input capture is not delivered source evidence", f.database.QueryRowContext(t.Context(), `SELECT count(*) FROM evidence_records WHERE session_id=?`, f.session).Scan(&observations))
	if envelopeInputs != 1 || children != 1 || observations != 0 || quota.detector != 0 || quota.memory != 0 {
		t.Fatalf("capture invented child/evidence or leaked detector scratch: envelope=%d children=%d observations=%d quota=%+v", envelopeInputs, children, observations, quota)
	}
	_, err = f.database.ExecContext(t.Context(), `DELETE FROM invocation_programs WHERE id=?`, f.program)
	testutil.FailErr(t, "release fixture durable references", err)
	testutil.FailErr(t, "collect actual inspection bodies", outputs.Close(t.Context()))
	if quota.state != 0 || quota.disk != 0 {
		t.Fatalf("inspection retention leaked leases: %+v", quota)
	}
}
