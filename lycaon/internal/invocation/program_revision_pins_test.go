package invocation_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func revisionPinsFixture() []toolcontract.ExecutionRevision {
	text := `{"name":"before"}` + "\n"
	disk := textfile.SHA256([]byte(text))
	return []toolcontract.ExecutionRevision{
		{Role: toolcontract.SourceRevision, Mode: toolcontract.AcceptedDocumentRevision, Basis: toolcontract.CapturedRevision, RootID: "root-1", Path: "config.json",
			DocumentID: "document-1", DocumentRevision: 3, DiskSHA256: disk, Representation: toolcontract.LogicalUTF8LF, ContentSHA256: disk, ContentBytes: int64(len(text))},
		{Role: toolcontract.DestinationRevision, Mode: toolcontract.AbsentRevision, Basis: toolcontract.CapturedRevision, RootID: "root-1", Path: "out/config.json"},
		{Role: toolcontract.SourceRevision, Mode: toolcontract.PublishedDiskRevision, Basis: toolcontract.DeclaredAtLaunch, RootID: "root-1", Path: "config.json",
			DiskSHA256: disk, Representation: toolcontract.OriginalEncodedBytes, ContentSHA256: disk, ContentBytes: int64(len(text))},
	}
}

func TestProgramReceiptsRecordExactRevisionPins(t *testing.T) {
	f := newProgramReceiptFixture(t)
	execution := f.childFor(t, "write").Invocation().Identity.ExecutionID
	pins := revisionPinsFixture()
	testutil.FailErr(t, "record captures before preparation", f.recorder.RecordRevisionPins(t.Context(), execution, pins[:2]))
	testutil.FailErr(t, "append launch declaration and repeat earlier captures", f.recorder.RecordRevisionPins(t.Context(), execution, pins))
	got, err := f.recorder.RevisionPins(t.Context(), f.session, execution)
	testutil.FailErr(t, "read recorded pins", err)
	if len(got) != len(pins) {
		t.Fatalf("pins = %+v", got)
	}
	for i := range pins {
		if got[i] != pins[i] {
			t.Fatalf("pin %d = %+v, want %+v", i, got[i], pins[i])
		}
	}
	moved := append([]toolcontract.ExecutionRevision(nil), pins...)
	moved[0].DocumentRevision = 4
	if err := f.recorder.RecordRevisionPins(t.Context(), execution, moved); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("refreshed capture accepted: %v", err)
	}
	if _, err := f.recorder.RevisionPins(t.Context(), "another-session", execution); err == nil {
		t.Fatal("pins crossed session scope")
	}
	result := toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect, Evidence: toolcontract.OperationEvidence{Owner: "filesystem", Kind: "result"}}
	testutil.FailErr(t, "settle child", f.recorder.SettleChild(t.Context(), execution, invocation.ChildSettlement{Result: result, DetailReference: "output:conflict"}))
	if err := f.recorder.RecordRevisionPins(t.Context(), execution, pins[:1]); err == nil {
		t.Fatal("settled execution gained pins")
	}
	got, err = f.recorder.RevisionPins(t.Context(), f.session, execution)
	testutil.FailErr(t, "pins survive settlement", err)
	if len(got) != len(pins) {
		t.Fatalf("settlement rewrote pins: %+v", got)
	}
}

func TestProgramReceiptsRefuseInvalidRevisionPins(t *testing.T) {
	f := newProgramReceiptFixture(t)
	execution := f.child(t).Invocation().Identity.ExecutionID
	valid := revisionPinsFixture()
	sourceAbsence := valid[1]
	sourceAbsence.Role = toolcontract.SourceRevision
	declaredDocument := valid[0]
	declaredDocument.Basis = toolcontract.DeclaredAtLaunch
	noContent := valid[0]
	noContent.ContentSHA256 = ""
	diskWithDocument := valid[2]
	diskWithDocument.Basis, diskWithDocument.DocumentID = toolcontract.CapturedRevision, "document-1"
	cases := map[string][]toolcontract.ExecutionRevision{
		"empty":                       nil,
		"absent source":               {sourceAbsence},
		"declared accepted document":  {declaredDocument},
		"missing content identity":    {noContent},
		"disk revision with document": {diskWithDocument},
		"missing role":                {{Mode: toolcontract.AbsentRevision, Basis: toolcontract.CapturedRevision, Path: "a"}},
		"missing path":                {{Role: toolcontract.DestinationRevision, Mode: toolcontract.AbsentRevision, Basis: toolcontract.CapturedRevision}},
		"unknown mode":                {{Role: toolcontract.DestinationRevision, Mode: "guessed", Basis: toolcontract.CapturedRevision, Path: "a"}},
		"missing basis":               {{Role: toolcontract.DestinationRevision, Mode: toolcontract.AbsentRevision, Path: "a"}},
	}
	for name, pins := range cases {
		t.Run(name, func(t *testing.T) {
			if err := f.recorder.RecordRevisionPins(t.Context(), execution, pins); err == nil {
				t.Fatal("invalid pin recorded")
			}
		})
	}
	if err := f.recorder.RecordRevisionPins(t.Context(), "missing-execution", valid); err == nil {
		t.Fatal("pins recorded without an execution")
	}
	got, err := f.recorder.RevisionPins(t.Context(), f.session, execution)
	testutil.FailErr(t, "read pins", err)
	if len(got) != 0 {
		t.Fatalf("refused pins persisted: %+v", got)
	}
}

func TestExecutionRevisionProjectsMappingPins(t *testing.T) {
	pins := revisionPinsFixture()
	document, ok := pins[0].SourcePin()
	if !ok || document.SourceID != "document:document-1" || document.Revision != "3" || document.SHA256 != pins[0].ContentSHA256 || document.EndByte != pins[0].ContentBytes {
		t.Fatalf("document pin = %+v", document)
	}
	disk, ok := pins[2].SourcePin()
	if !ok || disk.SourceID != "disk:root-1/config.json" || disk.Revision != "sha256:"+pins[2].DiskSHA256 || disk.Representation != toolcontract.OriginalEncodedBytes {
		t.Fatalf("disk pin = %+v", disk)
	}
	if _, ok := pins[1].SourcePin(); ok {
		t.Fatal("absence produced content")
	}
	if !pins[0].Same(pins[0]) || pins[0].Same(pins[2]) {
		t.Fatal("same-state comparison")
	}
}
