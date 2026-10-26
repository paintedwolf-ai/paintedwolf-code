package invocation_test

import (
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

func TestProgramSmallInputIsDurableInlineAndRecoversWithoutBlobClaim(t *testing.T) {
	f := newProgramReceiptFixture(t)
	quota := &inputCaptureQuota{}
	store := blobstore.Store{Root: t.TempDir()}
	outputs, err := invocation.NewProgramOutputRetention(t.Context(), f.recorder, store, f.session, quota)
	testutil.FailErr(t, "admit output lifetime", err)
	testutil.FailErr(t, "restore output lifetime", outputs.Restore(t.Context()))
	capture := invocation.ProgramInputCapture{Outputs: outputs, State: quota, Projector: captureprojection.New(secretmatch.NewInertMatcher(), nil), ProgramID: f.program, Scope: captureprojection.Scope{RootSessionID: f.session, SessionID: f.session}, Screening: captureprojection.StreamOptions{WorkingMemory: quota, Bounds: secretmatch.StreamBounds{InputBytes: 1 << 20, OutputBytes: 1 << 20, MemoryBytes: 32 << 20, DetectorWork: 1 << 30}}, MaximumNodes: 1024, MaximumDepth: 64}
	for _, value := range []string{"small", strings.Repeat("x", 17<<10)} {
		invoked := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: f.program, NodeID: "cat-node", ExecutionID: uuid.NewString(), ParentExecutionID: f.root}, ActorID: "fixture-person", SessionID: f.session, Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}, SourceUnitID: "submitted", Span: f.span, CatalogDigest: toolcontract.LanguageCatalogDigest()}
		review, releaseReview, err := toolcontract.NewReviewedOperationInputs(t.Context(), quota, map[string]any{"value": value})
		testutil.FailErr(t, "freeze expanded input", err)
		reference, err := capture.PrepareChild(t.Context(), toolcontract.ProgramChildStart{ParentReceiptID: f.parent, Invocation: invoked, Definition: f.definition, ExpandedInputs: review, Canonical: "read value"})
		releaseReview()
		testutil.FailErr(t, "prepare screened child", err)
		var inline []byte
		var blobJSON, claim string
		testutil.FailErr(t, "inspect durable input body", f.database.QueryRowContext(t.Context(), `SELECT inline_body,blob_json,retention_claim_id FROM invocation_program_outputs WHERE program_id=? AND reference=?`, f.program, reference).Scan(&inline, &blobJSON, &claim))
		if value == "small" {
			if len(inline) == 0 || blobJSON != "{}" || !strings.HasPrefix(claim, "inline:") {
				t.Fatalf("small input lacks exact inline receipt: body=%d blob=%q claim=%q", len(inline), blobJSON, claim)
			}
		} else if inline != nil || blobJSON == "{}" || strings.HasPrefix(claim, "inline:") {
			t.Fatalf("large input lost blob fallback: body=%d blob=%q claim=%q", len(inline), blobJSON, claim)
		}
		reader, err := outputs.Open(t.Context(), f.session, f.program, reference)
		testutil.FailErr(t, "open screened input", err)
		body, err := io.ReadAll(reader)
		testutil.FailErr(t, "read screened input", err)
		testutil.FailErr(t, "close screened input", reader.Close())
		if !strings.Contains(string(body), value) {
			t.Fatal("input body changed during retention")
		}
		if value == "small" && quota.disk != 0 {
			t.Fatal("inline input acquired a filesystem blob claim")
		}
	}
	recoveredQuota := &inputCaptureQuota{}
	recovered, err := invocation.NewProgramOutputRetention(t.Context(), f.recorder, store, f.session, recoveredQuota)
	testutil.FailErr(t, "admit recovered output lifetime", err)
	testutil.FailErr(t, "recover mixed inline and blob bodies", recovered.Restore(t.Context()))
	var reference string
	testutil.FailErr(t, "find inline reference", f.database.QueryRowContext(t.Context(), `SELECT reference FROM invocation_program_outputs WHERE program_id=? AND inline_body IS NOT NULL`, f.program).Scan(&reference))
	reader, err := recovered.Open(t.Context(), f.session, f.program, reference)
	testutil.FailErr(t, "open recovered inline body", err)
	body, err := io.ReadAll(reader)
	testutil.FailErr(t, "read recovered inline body", err)
	testutil.FailErr(t, "close recovered inline body", reader.Close())
	if !strings.Contains(string(body), "small") {
		t.Fatal("recovered inline body differs")
	}
	if _, err := recovered.Open(t.Context(), "foreign-session", f.program, reference); err == nil {
		t.Fatal("inline body crossed session scope")
	}
	_, err = f.database.ExecContext(t.Context(), `UPDATE invocation_program_outputs SET inline_body=x'00' WHERE program_id=? AND reference=?`, f.program, reference)
	testutil.FailErr(t, "corrupt one inline body", err)
	if _, err := recovered.Open(t.Context(), f.session, f.program, reference); err == nil {
		t.Fatal("inline body corruption passed its durable digest")
	}
}
