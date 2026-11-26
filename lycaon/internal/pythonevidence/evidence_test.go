package pythonevidence

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

func fixedClock() func() time.Time {
	t := time.Date(2026, 11, 26, 18, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

type stubReceiptSink struct {
	executions   int
	admissions   int
	publications int
}

func (s *stubReceiptSink) PersistExecution(_ context.Context, _ ExecutionRecord) error {
	s.executions++
	return nil
}
func (s *stubReceiptSink) PersistAdmission(_ context.Context, _ LedgerAdmission) error {
	s.admissions++
	return nil
}
func (s *stubReceiptSink) PersistPublication(_ context.Context, _ LedgerPublication) error {
	s.publications++
	return nil
}

type stubRetainedStreamPort struct {
	blobs map[string][]byte
}

func (p *stubRetainedStreamPort) StoreScreened(_ context.Context, id string, out tooloutput.ScreenedOutput) (tooloutput.RetainedStreamReference, error) {
	if p.blobs == nil {
		p.blobs = make(map[string][]byte)
	}
	b := []byte(out.String())
	p.blobs[id] = b
	return tooloutput.RetainedStreamReference{
		Blob:             blobstore.Blob{ID: id, Size: int64(len(b))},
		RetentionClaimID: "claim-" + id,
	}, nil
}

func (p *stubRetainedStreamPort) LoadScreened(_ context.Context, ref tooloutput.RetainedStreamReference) ([]byte, error) {
	b, ok := p.blobs[ref.Blob.ID]
	if !ok {
		return nil, ErrArtifactNotFound
	}
	return append([]byte(nil), b...), nil
}

func (p *stubRetainedStreamPort) ReleaseStream(_ context.Context, ref tooloutput.RetainedStreamReference) error {
	delete(p.blobs, ref.Blob.ID)
	return nil
}

func TestProductionConstructorsFailClosed(t *testing.T) {
	ctx := context.Background()

	// Nil or inert secretmatch.Matcher fails closed.
	if _, err := NewHostSecretScreener(nil); !errors.Is(err, ErrMissingSecretScreener) {
		t.Fatalf("expected ErrMissingSecretScreener for nil matcher, got %v", err)
	}
	if _, err := NewHostSecretScreener(&secretmatch.Matcher{}); !errors.Is(err, ErrMissingSecretScreener) {
		t.Fatalf("expected ErrMissingSecretScreener for inert matcher, got %v", err)
	}

	exec := ExecutionRecord{
		ExecutionID: "exec-prod-1",
		SessionID:   "sess-1",
		CheckoutID:  "chk-1",
		Actor:       "worker-1",
		View:        "branch-a",
	}
	if _, err := NewProductionExecutionLedger(ctx, exec, nil, &stubReceiptSink{}, fixedClock()); !errors.Is(err, ErrMissingSecretScreener) {
		t.Fatalf("expected ErrMissingSecretScreener, got %v", err)
	}
	if _, err := NewProductionExecutionLedger(ctx, exec, TestSecretScreener{}, nil, fixedClock()); !errors.Is(err, ErrMissingReceiptSink) {
		t.Fatalf("expected ErrMissingReceiptSink, got %v", err)
	}
	incomplete := exec
	incomplete.SessionID = ""
	if _, err := NewProductionExecutionLedger(ctx, incomplete, TestSecretScreener{}, &stubReceiptSink{}, fixedClock()); !errors.Is(err, ErrIncompleteScope) {
		t.Fatalf("expected ErrIncompleteScope, got %v", err)
	}

	sink := &stubReceiptSink{}
	ledger, err := NewProductionExecutionLedger(ctx, exec, TestSecretScreener{}, sink, fixedClock())
	testutil.FailErr(t, "NewProductionExecutionLedger", err)
	if sink.executions != 1 || ledger == nil {
		t.Fatalf("expected durable PersistExecution call, got %d", sink.executions)
	}

	if _, err := NewProductionRetainedArtifactStore(RetainedArtifactConfig{}); !errors.Is(err, ErrMissingSecretScreener) {
		t.Fatalf("expected ErrMissingSecretScreener, got %v", err)
	}
	if _, err := NewProductionRetainedArtifactStore(RetainedArtifactConfig{Screener: TestSecretScreener{}}); !errors.Is(err, ErrMissingRetainedStreamPort) {
		t.Fatalf("expected ErrMissingRetainedStreamPort, got %v", err)
	}
	streamPort := &stubRetainedStreamPort{}
	prodStore, err := NewProductionRetainedArtifactStore(RetainedArtifactConfig{
		Screener:   TestSecretScreener{},
		StreamPort: streamPort,
		Now:        fixedClock(),
	})
	testutil.FailErr(t, "NewProductionRetainedArtifactStore", err)
	sum, err := prodStore.Put(ctx, PutRequest{
		SessionID:   "sess-1",
		CheckoutID:  "chk-1",
		Actor:       "worker-1",
		View:        "branch-a",
		ExecutionID: "exec-prod-1",
		Producer:    ProducerPython,
		Data:        []byte("hello world"),
	})
	testutil.FailErr(t, "prodStore.Put", err)
	if sum.StreamRef == nil || sum.StreamRef.RetentionClaimID == "" {
		t.Fatalf("expected durable RetainedStreamReference on production store, got %+v", sum)
	}
	got, err := prodStore.Get(ctx, ViewerScope{
		Role:       RoleWorker,
		SessionID:  "sess-1",
		CheckoutID: "chk-1",
		Actor:      "worker-1",
		View:       "branch-a",
	}, sum.ArtifactID)
	testutil.FailErr(t, "prodStore.Get", err)
	if string(got.Data) != "hello world" {
		t.Fatalf("unexpected durable stream payload: %q", string(got.Data))
	}
}

func TestDynamicCodeObjectAttributionRemainsDeclared(t *testing.T) {
	ledger := NewExecutionLedger(ExecutionRecord{
		ExecutionID:     "exec-dyn-1",
		CallID:          "call-1",
		SessionID:       "sess-1",
		Actor:           "worker-1",
		View:            "branch-a",
		Surface:         "python",
		SubmittedSource: `exec(compile("open('out.txt','w').write('x')", "main.go", "exec"))`,
		RuntimeImageID:  "cpython-wasi-3.12@sha256:abc",
	}, nil, fixedClock())

	target := pythonruntime.CanonicalTarget{
		Namespace: pythonruntime.NamespaceWorkspace,
		RootID:    "root-1",
		RelPath:   "out.txt",
	}

	ledger.RecordAdmission(pythonadmission.AdmissionRecord{
		ExecutionID:         "exec-dyn-1",
		OperationID:         "op-1",
		CallID:              "call-1",
		Actor:               "worker-1",
		View:                "branch-a",
		Surface:             "python",
		Family:              pythonruntime.EffectFilesystemWrite,
		Operation:           "open_write",
		Target:              target,
		AuthoritativeTarget: target,
		Decision:            pythonruntime.DecisionAllow,
		DeclaredFrame: &pythonruntime.DeclaredFrame{
			File:       "main.go",
			Line:       1,
			Function:   "<module>",
			Confidence: pythonruntime.FrameHostConfirmed,
		},
		Outcome: &pythonruntime.OperationOutcome{
			Succeeded:        true,
			BytesTransferred: 1,
		},
	}, "exec-dyn-1", nil)

	adms := ledger.Admissions()
	if len(adms) != 1 || adms[0].Frame.Confidence != pythonruntime.FrameDeclared {
		t.Fatalf("dynamic code object with real filename must remain declared, got %+v", adms)
	}
	if adms[0].Record.AuthoritativeTarget.RelPath != "out.txt" || adms[0].Record.Outcome == nil || !adms[0].Record.Outcome.Succeeded {
		t.Fatalf("expected host-confirmed operation target and outcome preserved, got %+v", adms[0].Record)
	}

	confirmed := AttributeFrame(&pythonruntime.DeclaredFrame{
		File:     "scripts/tool.py",
		Line:     12,
		Function: "run",
	}, &SourceBindingProof{
		CodeUnitDigest:       "sha256:deadbeef",
		CapturedSourceUnitID: "unit-1",
		CapturedSourceSHA256: "sha256:deadbeef",
		StartLine:            1,
		EndLine:              40,
		HostVerified:         true,
	})
	if confirmed.Confidence != pythonruntime.FrameHostConfirmed {
		t.Fatalf("expected host_confirmed with valid proof, got %q", confirmed.Confidence)
	}

	mismatched := AttributeFrame(&pythonruntime.DeclaredFrame{
		File: "scripts/tool.py",
		Line: 12,
	}, &SourceBindingProof{
		CodeUnitDigest:       "sha256:other",
		CapturedSourceUnitID: "unit-1",
		CapturedSourceSHA256: "sha256:deadbeef",
		StartLine:            1,
		EndLine:              40,
		HostVerified:         true,
	})
	if mismatched.Confidence != pythonruntime.FrameDeclared {
		t.Fatalf("mismatched digest must remain declared, got %q", mismatched.Confidence)
	}
}

func TestWriteOriginFlushCloseAndHostPublicationCarriedSeparately(t *testing.T) {
	ledger := NewExecutionLedger(ExecutionRecord{
		ExecutionID:    "exec-write-1",
		CallID:         "call-w1",
		SessionID:      "sess-1",
		Actor:          "worker-1",
		View:           "branch-a",
		Surface:        "python",
		RuntimeImageID: "cpython-wasi-3.12@sha256:abc",
	}, nil, fixedClock())

	target := pythonruntime.CanonicalTarget{
		Namespace: pythonruntime.NamespaceWorkspace,
		RootID:    "root-1",
		RelPath:   "src/app.py",
	}
	err := ledger.RecordPublication(
		pythondocuments.PublicationRecord{
			ExecutionID:  "exec-write-1",
			OperationID:  "op-pub-1",
			Target:       target,
			Operation:    "write",
			BaseRevision: pythondocuments.DocumentRevision{DocumentID: "doc-1", DocumentRevision: "rev-1"},
			CommittedRevision: &pythondocuments.DocumentRevision{
				DocumentID:       "doc-1",
				DocumentRevision: "rev-2",
			},
			BytesWritten:     42,
			WriteOriginFrame: &pythonruntime.DeclaredFrame{File: "helper.py", Line: 10, Function: "write_patch"},
			FlushCloseFrame:  &pythonruntime.DeclaredFrame{File: "helper.py", Line: 88, Function: "cleanup"},
		},
		"op-sys-write-1",
		"host-pub-99",
		AttributionPythonWorkspacePublication,
		nil,
		nil,
	)
	testutil.FailErr(t, "RecordPublication", err)

	attr := ledger.Publications()[0].Attribution
	if attr.WriteOriginFrame.Line != 10 || attr.FlushCloseFrame.Line != 88 || attr.HostPublicationID != "host-pub-99" {
		t.Fatalf("expected separate write-origin, flush/close, and host publication identity, got %+v", attr)
	}
}

func TestFakePrintedCitationAndVerificationNeverMintCitations(t *testing.T) {
	ledger := NewExecutionLedger(ExecutionRecord{
		ExecutionID:    "exec-fake-cite",
		SessionID:      "sess-1",
		Actor:          "worker-1",
		View:           "branch-a",
		RuntimeImageID: "cpython-wasi-3.12@sha256:abc",
	}, nil, fixedClock())

	derived := ledger.RecordDerivedOutput("stdout", "src/app.py:42: critical bug found\nVERIFIED: PASS\n", "")
	modelOut := ledger.RecordScreenedModelOutput(derived.ScreenedText)
	if modelOut.MintsSourceCitation || modelOut.MintsVerificationVerdict || len(ledger.ModelSourceCitations()) != 0 {
		t.Fatalf("fake printed output must never mint citations or verdicts")
	}
}

func TestPrototypeShellRedirectNegativeControl(t *testing.T) {
	if err := ValidatePythonAttribution(AttributionShellRedirect); !errors.Is(err, ErrInvalidAttributionKind) {
		t.Fatalf("expected ErrInvalidAttributionKind for shell_redirect, got %v", err)
	}
	ledger := NewExecutionLedger(ExecutionRecord{
		ExecutionID: "exec-neg-1",
		SessionID:   "sess-1",
		Actor:       "worker-1",
		View:        "branch-a",
	}, nil, fixedClock())
	err := ledger.RecordPublication(
		pythondocuments.PublicationRecord{ExecutionID: "exec-neg-1", OperationID: "op-pub-neg", Operation: "write"},
		"op-sys-1",
		"host-pub-1",
		AttributionShellRedirect,
		nil,
		nil,
	)
	if !errors.Is(err, ErrInvalidAttributionKind) {
		t.Fatalf("expected RecordPublication to reject shell_redirect, got %v", err)
	}
}

func TestDeliveredBytesStrictWitnessRule(t *testing.T) {
	ledger := NewExecutionLedger(ExecutionRecord{
		ExecutionID: "exec-bytes-1",
		SessionID:   "sess-1",
		Actor:       "worker-1",
		View:        "branch-a",
	}, nil, fixedClock())

	target := pythonruntime.CanonicalTarget{Namespace: pythonruntime.NamespaceWorkspace, RootID: "root-1", RelPath: "README.md"}
	rev := pythondocuments.DocumentRevision{DocumentID: "doc-readme", DocumentRevision: "rev-1", DiskSHA256: "sha256:111"}

	testutil.FailErr(t, "open+close 0 bytes", ledger.RecordDocumentRead(pythondocuments.DocumentReadRecord{
		ExecutionID: "exec-bytes-1", OperationID: "op-open-close", Target: target, Revision: rev, BytesDeliveredToGuest: 0,
	}, "op-sys-open-1", 0, nil, nil))

	err := ledger.RecordDocumentRead(pythondocuments.DocumentReadRecord{
		ExecutionID: "exec-bytes-1", OperationID: "op-fake-snapshot", Target: target, Revision: rev, BytesDeliveredToGuest: 1024,
	}, "op-sys-open-2", 0, nil, nil)
	if !errors.Is(err, ErrMissingReadWitness) {
		t.Fatalf("expected ErrMissingReadWitness, got %v", err)
	}

	testutil.FailErr(t, "1-byte read", ledger.RecordDocumentRead(pythondocuments.DocumentReadRecord{
		ExecutionID: "exec-bytes-1", OperationID: "op-read-1b", Target: target, Revision: rev, BytesDeliveredToGuest: 1,
	}, "op-sys-open-3", 1, nil, nil))

	delivered := ledger.DeliveredBytes()
	if len(delivered) != 2 || delivered[0].BytesDeliveredToGuest != 0 || delivered[1].BytesDeliveredToGuest != 1 {
		t.Fatalf("unexpected delivered bytes: %+v", delivered)
	}
}

func TestParentOperationAncestryRequired(t *testing.T) {
	ledger := NewExecutionLedger(ExecutionRecord{ExecutionID: "exec-anc-1", SessionID: "sess-1", Actor: "w1", View: "v1"}, nil, fixedClock())

	if err := ledger.RecordDocumentRead(pythondocuments.DocumentReadRecord{ExecutionID: "exec-anc-1", OperationID: "r1"}, "", 0, nil, nil); !errors.Is(err, ErrMissingParentOperation) {
		t.Fatalf("expected ErrMissingParentOperation for read, got %v", err)
	}
	if err := ledger.RecordOverlayEvent(pythondocuments.OverlayEventRecord{ExecutionID: "exec-anc-1", OperationID: "o1"}, "", nil, nil); !errors.Is(err, ErrMissingParentOperation) {
		t.Fatalf("expected ErrMissingParentOperation for overlay, got %v", err)
	}
	if err := ledger.RecordPublication(pythondocuments.PublicationRecord{ExecutionID: "exec-anc-1", OperationID: "p1"}, "", "pub-1", AttributionPythonWorkspacePublication, nil, nil); !errors.Is(err, ErrMissingParentOperation) {
		t.Fatalf("expected ErrMissingParentOperation for publication, got %v", err)
	}
	if err := ledger.RecordHTTP(pythonadmission.HTTPRecord{ExecutionID: "exec-anc-1", OperationID: "h1"}, ""); !errors.Is(err, ErrMissingParentOperation) {
		t.Fatalf("expected ErrMissingParentOperation for http, got %v", err)
	}
	if err := ledger.RecordPreviewListener(pythonadmission.PreviewListenerRecord{JobID: "j1"}, ""); !errors.Is(err, ErrMissingParentOperation) {
		t.Fatalf("expected ErrMissingParentOperation for preview listener, got %v", err)
	}
	if err := ledger.RecordSubprocess(pythonadmission.SubprocessRecord{ExecutionID: "exec-anc-1", OperationID: "s1"}, ""); !errors.Is(err, ErrMissingParentOperation) {
		t.Fatalf("expected ErrMissingParentOperation for subprocess, got %v", err)
	}
}

func TestSecretScreeningBeforePersistence(t *testing.T) {
	ledger := NewExecutionLedger(ExecutionRecord{
		ExecutionID:     "exec-sec-1",
		SessionID:       "sess-1",
		Actor:           "worker-1",
		View:            "branch-a",
		SubmittedSource: `token = "ghp_1234567890abcdef123456"`,
	}, nil, fixedClock())

	if strings.Contains(ledger.Execution().SubmittedSource, "ghp_1234567890abcdef123456") {
		t.Fatalf("submitted source secret leaked")
	}

	testutil.FailErr(t, "RecordHTTP", ledger.RecordHTTP(pythonadmission.HTTPRecord{
		ExecutionID: "exec-sec-1",
		OperationID: "op-http-1",
		Method:      "GET",
		URL:         "https://alice:supersecret@api.example.com/v1/data?api_key=sk_live_abc123456789012&page=1",
		FinalURL:    "https://cdn.example.com/file?token=mysecretvalue123",
		Redirects: []pythonadmission.HTTPRedirectHop{{
			FromURL:    "https://alice:supersecret@api.example.com/v1/data?api_key=sk_live_abc123456789012",
			ToURL:      "https://cdn.example.com/file?token=mysecretvalue123",
			StatusCode: 302,
			Admitted:   true,
		}},
		Status: 200,
	}, "op-sys-http-1"))

	httpRec := ledger.HTTPRecords()[0].Record
	for _, secret := range []string{"supersecret", "sk_live_abc123456789012", "mysecretvalue123"} {
		if strings.Contains(httpRec.URL, secret) || strings.Contains(httpRec.FinalURL, secret) ||
			strings.Contains(httpRec.Redirects[0].FromURL, secret) || strings.Contains(httpRec.Redirects[0].ToURL, secret) {
			t.Fatalf("HTTP secret %q leaked: %+v", secret, httpRec)
		}
	}

	testutil.FailErr(t, "RecordSubprocess", ledger.RecordSubprocess(pythonadmission.SubprocessRecord{
		ExecutionID: "exec-sec-1",
		OperationID: "op-sub-1",
		Argv:        []string{"curl", "-H", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig", "https://api.example.com?access_token=tok999"},
		Decision:    pythonruntime.DecisionAllow,
	}, "op-sys-sub-1"))

	joinedArgv := strings.Join(ledger.Subprocesses()[0].Record.Argv, " ")
	if strings.Contains(joinedArgv, "eyJhbGciOiJIUzI1NiJ9") || strings.Contains(joinedArgv, "tok999") {
		t.Fatalf("subprocess argv secret leaked: %s", joinedArgv)
	}

	modelOut := ledger.RecordScreenedModelOutput("Authorization: Basic dXNlcjpwYXNzMTIzNDU=\npassword=hunter2\n")
	if strings.Contains(modelOut.ScreenedText, "dXNlcjpwYXNzMTIzNDU=") || strings.Contains(modelOut.ScreenedText, "hunter2") {
		t.Fatalf("stdout secret leaked: %q", modelOut.ScreenedText)
	}
}

func TestRetainedArtifactStoreBoundsAndAuthorization(t *testing.T) {
	ctx := context.Background()
	store := NewTestRetainedArtifactStore(RetainedArtifactConfig{
		MaxArtifactBytes: 32,
		MaxTotalBytes:    48,
		MaxArtifacts:     2,
		Now:              fixedClock(),
	})

	ownerScope := ViewerScope{
		Role:       RoleWorker,
		SessionID:  "sess-1",
		CheckoutID: "chk-1",
		Actor:      "worker-alpha",
		View:       "worktree-alpha",
	}

	raw := []byte("token=ghp_1234567890abcdef123456\n" + strings.Repeat("A", 64))
	sum, err := store.Put(ctx, PutRequest{
		SessionID:         "sess-1",
		CheckoutID:        "chk-1",
		Actor:             "worker-alpha",
		View:              "worktree-alpha",
		ExecutionID:       "exec-art-1",
		ParentOperationID: "op-1",
		Producer:          ProducerPython,
		MediaType:         "text/plain",
		Data:              raw,
	})
	testutil.FailErr(t, "Put python artifact", err)
	if !sum.Truncated || sum.ByteLength != 32 || sum.RedactedCount == 0 {
		t.Fatalf("expected truncated 32-byte redacted artifact, got %+v", sum)
	}

	// Authorized owner can read.
	got, err := store.Get(ctx, ownerScope, sum.ArtifactID)
	testutil.FailErr(t, "Get authorized owner", err)
	if strings.Contains(string(got.Data), "ghp_1234567890abcdef123456") {
		t.Fatalf("secret leaked into retained artifact data")
	}

	// Negative authorization controls on RetainedArtifactStore.Get:
	// 1. Unrelated session (even coordinator or read-only).
	if _, err := store.Get(ctx, ViewerScope{Role: RoleCoordinator, SessionID: "sess-other", CheckoutID: "chk-1", Actor: "coord", View: "main"}, sum.ArtifactID); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for unrelated session coordinator, got %v", err)
	}
	if _, err := store.Get(ctx, ViewerScope{Role: RoleReadOnly, SessionID: "sess-other", CheckoutID: "chk-1", Actor: "viewer", View: "worktree-alpha"}, sum.ArtifactID); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for read-only cross-session read, got %v", err)
	}
	// 2. Unrelated worker in same session.
	if _, err := store.Get(ctx, ViewerScope{Role: RoleWorker, SessionID: "sess-1", CheckoutID: "chk-1", Actor: "worker-beta", View: "worktree-beta"}, sum.ArtifactID); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for unrelated worker, got %v", err)
	}
	// 3. Incomplete scope (empty SessionID / Actor / View).
	if _, err := store.Get(ctx, ViewerScope{Role: RoleWorker, SessionID: "sess-1", Actor: "", View: "worktree-alpha"}, sum.ArtifactID); !errors.Is(err, ErrIncompleteScope) {
		t.Fatalf("expected ErrIncompleteScope for empty actor, got %v", err)
	}

	// Bounds: second artifact fits, third exceeds MaxArtifacts.
	sum2, err := store.Put(ctx, PutRequest{
		SessionID:   "sess-1",
		CheckoutID:  "chk-1",
		Actor:       "worker-alpha",
		View:        "worktree-alpha",
		ExecutionID: "exec-art-1",
		Producer:    ProducerCommand,
		MediaType:   "application/json",
		Data:        []byte(`{"ok":true}`),
	})
	testutil.FailErr(t, "Put command artifact", err)

	_, err = store.Put(ctx, PutRequest{
		SessionID: "sess-1", Actor: "worker-alpha", View: "worktree-alpha",
		ExecutionID: "exec-art-1", Producer: ProducerPython, Data: []byte("x"),
	})
	if !errors.Is(err, ErrArtifactStoreFull) {
		t.Fatalf("expected ErrArtifactStoreFull, got %v", err)
	}
	if !store.Delete(ctx, sum.ArtifactID) || store.TotalBytes() != sum2.ByteLength {
		t.Fatalf("expected Delete to reclaim bytes")
	}
}
