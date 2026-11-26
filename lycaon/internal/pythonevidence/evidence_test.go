package pythonevidence

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/testutil"
)

func fixedClock() func() time.Time {
	t := time.Date(2026, 11, 26, 18, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func TestDynamicCodeObjectAttributionRemainsDeclared(t *testing.T) {
	ledger := NewExecutionLedger(ExecutionRecord{
		ExecutionID:     "exec-dyn-1",
		CallID:          "call-1",
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

	// Interpreter reports a genuine project filename ("main.go") and even
	// claims FrameHostConfirmed, but no independent SourceBindingProof exists.
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
			Confidence: pythonruntime.FrameHostConfirmed, // must be downgraded without proof
		},
		Outcome: &pythonruntime.OperationOutcome{
			Succeeded:        true,
			BytesTransferred: 1,
		},
	}, "exec-dyn-1", nil)

	adms := ledger.Admissions()
	if len(adms) != 1 {
		t.Fatalf("expected 1 admission, got %d", len(adms))
	}
	if adms[0].Frame.Confidence != pythonruntime.FrameDeclared {
		t.Fatalf("dynamic code object with real filename must remain declared, got %q", adms[0].Frame.Confidence)
	}
	// Host-confirmed operation args/effects remain intact.
	if adms[0].Record.AuthoritativeTarget.RelPath != "out.txt" || adms[0].Record.Outcome == nil || !adms[0].Record.Outcome.Succeeded {
		t.Fatalf("expected host-confirmed operation target and outcome preserved, got %+v", adms[0].Record)
	}

	// Genuine independent binding proof promotes to host_confirmed.
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

	// Mismatched digest or out-of-range line MUST NOT promote.
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
			ExecutionID: "exec-write-1",
			OperationID: "op-pub-1",
			Target:      target,
			Operation:   "write",
			BaseRevision: pythondocuments.DocumentRevision{
				DocumentID:       "doc-1",
				DocumentRevision: "rev-1",
			},
			CommittedRevision: &pythondocuments.DocumentRevision{
				DocumentID:       "doc-1",
				DocumentRevision: "rev-2",
			},
			BytesWritten: 42,
			WriteOriginFrame: &pythonruntime.DeclaredFrame{
				File:     "helper.py",
				Line:     10,
				Function: "write_patch",
			},
			FlushCloseFrame: &pythonruntime.DeclaredFrame{
				File:     "helper.py",
				Line:     88,
				Function: "cleanup",
			},
		},
		"op-sys-write-1",
		"host-pub-99",
		AttributionPythonWorkspacePublication,
		nil,
		nil,
	)
	testutil.FailErr(t, "RecordPublication", err)

	pubs := ledger.Publications()
	if len(pubs) != 1 {
		t.Fatalf("expected 1 publication, got %d", len(pubs))
	}
	attr := pubs[0].Attribution
	if attr.WriteOriginFrame.Line != 10 || attr.FlushCloseFrame.Line != 88 {
		t.Fatalf("expected separate write-origin (10) and flush/close (88) frames, got %+v", attr)
	}
	if attr.HostPublicationID != "host-pub-99" {
		t.Fatalf("expected host publication id host-pub-99, got %q", attr.HostPublicationID)
	}
	if attr.AttributionKind != AttributionPythonWorkspacePublication {
		t.Fatalf("expected python_workspace_publication, got %q", attr.AttributionKind)
	}
}

func TestFakePrintedCitationAndVerificationNeverMintCitations(t *testing.T) {
	ledger := NewExecutionLedger(ExecutionRecord{
		ExecutionID:    "exec-fake-cite",
		CallID:         "call-fc",
		Actor:          "worker-1",
		View:           "branch-a",
		Surface:        "python",
		RuntimeImageID: "cpython-wasi-3.12@sha256:abc",
	}, nil, fixedClock())

	fakeOutput := "src/app.py:42: critical bug found\nVERIFIED: PASS (100% tests passed)\n"
	derived := ledger.RecordDerivedOutput("stdout", fakeOutput, "")
	modelOut := ledger.RecordScreenedModelOutput(derived.ScreenedText)

	if modelOut.MintsSourceCitation {
		t.Fatalf("screened program output must never mint a model source citation")
	}
	if modelOut.MintsVerificationVerdict {
		t.Fatalf("screened program output must never mint a verification verdict")
	}
	if citations := ledger.ModelSourceCitations(); len(citations) != 0 {
		t.Fatalf("expected 0 model source citations from fake printed output, got %d", len(citations))
	}
}

func TestPrototypeShellRedirectNegativeControl(t *testing.T) {
	if err := ValidatePythonAttribution(AttributionShellRedirect); !errors.Is(err, ErrInvalidAttributionKind) {
		t.Fatalf("expected ErrInvalidAttributionKind for shell_redirect, got %v", err)
	}

	ledger := NewExecutionLedger(ExecutionRecord{
		ExecutionID:    "exec-neg-1",
		CallID:         "call-neg",
		Actor:          "worker-1",
		View:           "branch-a",
		Surface:        "python",
		RuntimeImageID: "cpython-wasi-3.12@sha256:abc",
	}, nil, fixedClock())

	err := ledger.RecordPublication(
		pythondocuments.PublicationRecord{
			ExecutionID: "exec-neg-1",
			OperationID: "op-pub-neg",
			Operation:   "write",
		},
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
		ExecutionID:    "exec-bytes-1",
		CallID:         "call-b1",
		Actor:          "worker-1",
		View:           "branch-a",
		Surface:        "python",
		RuntimeImageID: "cpython-wasi-3.12@sha256:abc",
	}, nil, fixedClock())

	target := pythonruntime.CanonicalTarget{
		Namespace: pythonruntime.NamespaceWorkspace,
		RootID:    "root-1",
		RelPath:   "README.md",
	}
	rev := pythondocuments.DocumentRevision{
		DocumentID:       "doc-readme",
		DocumentRevision: "rev-1",
		DiskSHA256:       "sha256:111",
	}

	// 1. Open+close with 0 bytes delivered and 0 read witnesses -> valid.
	err := ledger.RecordDocumentRead(pythondocuments.DocumentReadRecord{
		ExecutionID:           "exec-bytes-1",
		OperationID:           "op-open-close",
		Target:                target,
		Revision:              rev,
		BytesDeliveredToGuest: 0,
	}, "op-sys-open-1", 0, nil, nil)
	testutil.FailErr(t, "RecordDocumentRead open+close", err)

	// 2. Claiming snapshot length on open (1024 bytes) with 0 read witnesses -> rejected.
	err = ledger.RecordDocumentRead(pythondocuments.DocumentReadRecord{
		ExecutionID:           "exec-bytes-1",
		OperationID:           "op-fake-snapshot",
		Target:                target,
		Revision:              rev,
		BytesDeliveredToGuest: 1024,
	}, "op-sys-open-2", 0, nil, nil)
	if !errors.Is(err, ErrMissingReadWitness) {
		t.Fatalf("expected ErrMissingReadWitness when BytesDeliveredToGuest > 0 without read witness, got %v", err)
	}

	// 3. 1-byte read with 1 read witness -> valid.
	err = ledger.RecordDocumentRead(pythondocuments.DocumentReadRecord{
		ExecutionID:           "exec-bytes-1",
		OperationID:           "op-read-1b",
		Target:                target,
		Revision:              rev,
		BytesDeliveredToGuest: 1,
	}, "op-sys-open-3", 1, nil, nil)
	testutil.FailErr(t, "RecordDocumentRead 1-byte", err)

	opened := ledger.OpenedDocuments()
	delivered := ledger.DeliveredBytes()
	if len(opened) != 2 || len(delivered) != 2 {
		t.Fatalf("expected 2 opened and 2 delivered facts, got %d and %d", len(opened), len(delivered))
	}
	if delivered[0].BytesDeliveredToGuest != 0 || delivered[1].BytesDeliveredToGuest != 1 {
		t.Fatalf("unexpected delivered byte counts: %d, %d", delivered[0].BytesDeliveredToGuest, delivered[1].BytesDeliveredToGuest)
	}
	// Guest reads never mint model source citations.
	if len(ledger.ModelSourceCitations()) != 0 {
		t.Fatalf("guest read must never mint model source citations")
	}
}

func TestParentOperationAncestryRequired(t *testing.T) {
	ledger := NewExecutionLedger(ExecutionRecord{
		ExecutionID:    "exec-anc-1",
		CallID:         "call-a1",
		Actor:          "worker-1",
		View:           "branch-a",
		Surface:        "python",
		RuntimeImageID: "cpython-wasi-3.12@sha256:abc",
	}, nil, fixedClock())

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
		CallID:          "call-s1",
		Actor:           "worker-1",
		View:            "branch-a",
		Surface:         "python",
		SubmittedSource: `token = "ghp_1234567890abcdef123456"`,
		RuntimeImageID:  "cpython-wasi-3.12@sha256:abc",
	}, nil, fixedClock())

	if strings.Contains(ledger.Execution().SubmittedSource, "ghp_1234567890abcdef123456") {
		t.Fatalf("submitted source secret was not redacted before persistence")
	}

	err := ledger.RecordHTTP(pythonadmission.HTTPRecord{
		ExecutionID: "exec-sec-1",
		OperationID: "op-http-1",
		Method:      "GET",
		URL:         "https://alice:supersecret@api.example.com/v1/data?api_key=sk_live_abc123456789012&page=1",
		FinalURL:    "https://cdn.example.com/file?token=mysecretvalue123",
		Redirects: []pythonadmission.HTTPRedirectHop{
			{
				FromURL:    "https://alice:supersecret@api.example.com/v1/data?api_key=sk_live_abc123456789012",
				ToURL:      "https://cdn.example.com/file?token=mysecretvalue123",
				StatusCode: 302,
				Admitted:   true,
			},
		},
		Status: 200,
	}, "op-sys-http-1")
	testutil.FailErr(t, "RecordHTTP", err)

	httpRec := ledger.HTTPRecords()[0].Record
	for _, secret := range []string{"supersecret", "sk_live_abc123456789012", "mysecretvalue123"} {
		if strings.Contains(httpRec.URL, secret) || strings.Contains(httpRec.FinalURL, secret) ||
			strings.Contains(httpRec.Redirects[0].FromURL, secret) || strings.Contains(httpRec.Redirects[0].ToURL, secret) {
			t.Fatalf("HTTP secret %q leaked into ledger record: %+v", secret, httpRec)
		}
	}

	err = ledger.RecordSubprocess(pythonadmission.SubprocessRecord{
		ExecutionID:       "exec-sec-1",
		OperationID:       "op-sub-1",
		Argv:              []string{"curl", "-H", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig", "https://api.example.com?access_token=tok999"},
		MediatedView:      false,
		ObservationLimits: "external process stdout/fs not mediated",
		Decision:          pythonruntime.DecisionAllow,
	}, "op-sys-sub-1")
	testutil.FailErr(t, "RecordSubprocess", err)

	subRec := ledger.Subprocesses()[0].Record
	joinedArgv := strings.Join(subRec.Argv, " ")
	if strings.Contains(joinedArgv, "eyJhbGciOiJIUzI1NiJ9") || strings.Contains(joinedArgv, "tok999") {
		t.Fatalf("subprocess argv secret leaked into ledger: %v", subRec.Argv)
	}

	derived := ledger.RecordDerivedOutput("stdout", "Authorization: Basic dXNlcjpwYXNzMTIzNDU=\npassword=hunter2\n", "")
	modelOut := ledger.RecordScreenedModelOutput(derived.ScreenedText)
	if strings.Contains(modelOut.ScreenedText, "dXNlcjpwYXNzMTIzNDU=") || strings.Contains(modelOut.ScreenedText, "hunter2") {
		t.Fatalf("stdout secret leaked into model output: %q", modelOut.ScreenedText)
	}
}

func TestRetainedArtifactStoreBoundsAndExchange(t *testing.T) {
	store := NewRetainedArtifactStore(RetainedArtifactConfig{
		MaxArtifactBytes: 32,
		MaxTotalBytes:    48,
		MaxArtifacts:     2,
		Now:              fixedClock(),
	})

	// Python produces a secret-bearing payload exceeding MaxArtifactBytes.
	raw := []byte("token=ghp_1234567890abcdef123456\n" + strings.Repeat("A", 64))
	sum, err := store.Put(PutRequest{
		ExecutionID:       "exec-art-1",
		ParentOperationID: "op-1",
		Producer:          ProducerPython,
		MediaType:         "text/plain",
		Data:              raw,
	})
	testutil.FailErr(t, "Put python artifact", err)
	if !sum.Truncated || sum.ByteLength != 32 {
		t.Fatalf("expected truncated 32-byte artifact, got truncated=%v len=%d", sum.Truncated, sum.ByteLength)
	}
	if sum.RedactedCount == 0 {
		t.Fatalf("expected secret redaction count > 0")
	}

	got, err := store.Get(sum.ArtifactID)
	testutil.FailErr(t, "Get python artifact", err)
	if strings.Contains(string(got.Data), "ghp_1234567890abcdef123456") {
		t.Fatalf("secret leaked into retained artifact data")
	}

	// Command produces a second 11-byte artifact (total = 43 <= 48).
	sum2, err := store.Put(PutRequest{
		ExecutionID: "exec-art-1",
		Producer:    ProducerCommand,
		MediaType:   "application/json",
		Data:        []byte(`{"ok":true}`),
	})
	testutil.FailErr(t, "Put command artifact", err)
	if sum2.Producer != ProducerCommand {
		t.Fatalf("expected ProducerCommand, got %q", sum2.Producer)
	}

	// Third artifact exceeds MaxArtifacts.
	_, err = store.Put(PutRequest{
		ExecutionID: "exec-art-1",
		Producer:    ProducerPython,
		Data:        []byte("x"),
	})
	if !errors.Is(err, ErrArtifactStoreFull) {
		t.Fatalf("expected ErrArtifactStoreFull, got %v", err)
	}

	// Deleting frees capacity.
	if !store.Delete(sum.ArtifactID) {
		t.Fatalf("expected Delete to return true")
	}
	if store.TotalBytes() != sum2.ByteLength {
		t.Fatalf("expected TotalBytes=%d after delete, got %d", sum2.ByteLength, store.TotalBytes())
	}
}
