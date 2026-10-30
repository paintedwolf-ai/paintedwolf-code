package invocation_test

import (
	"fmt"
	"strings"
	"testing"

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
	testutil.FailErr(t, "record captures before preparation", f.recorder.RecordRevisionPins(t.Context(), execution, 0, pins[:2]))
	testutil.FailErr(t, "append launch declaration and repeat earlier captures", f.recorder.RecordRevisionPins(t.Context(), execution, 0, pins))
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
	if err := f.recorder.RecordRevisionPins(t.Context(), execution, 0, moved); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("refreshed capture accepted: %v", err)
	}
	if _, err := f.recorder.RevisionPins(t.Context(), "another-session", execution); err == nil {
		t.Fatal("pins crossed session scope")
	}
	result := toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect, Evidence: toolcontract.OperationEvidence{Owner: "filesystem", Kind: "result"}}
	testutil.FailErr(t, "settle child", f.recorder.SettleChild(t.Context(), execution, toolcontract.ProgramChildSettlement{Result: result, DetailReference: "output:conflict"}))
	if err := f.recorder.RecordRevisionPins(t.Context(), execution, 3, pins[:1]); err == nil {
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
			if err := f.recorder.RecordRevisionPins(t.Context(), execution, 0, pins); err == nil {
				t.Fatal("invalid pin recorded")
			}
		})
	}
	if err := f.recorder.RecordRevisionPins(t.Context(), "missing-execution", 0, valid); err == nil {
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

// Independent owners of one execution append without assigning ordinals:
// a redirect destination, a second destination and a later source capture.
func TestProgramReceiptsAppendRevisionPinsInCaptureOrder(t *testing.T) {
	f := newProgramReceiptFixture(t)
	execution := f.childFor(t, "write").Invocation().Identity.ExecutionID
	pins := revisionPinsFixture()
	for _, pin := range []toolcontract.ExecutionRevision{pins[1], pins[1], pins[0]} {
		testutil.FailErr(t, "append capture", f.recorder.AppendRevisionPin(t.Context(), execution, pin))
	}
	testutil.FailErr(t, "resubmit the recorded prefix", f.recorder.RecordRevisionPins(t.Context(), execution, 0, []toolcontract.ExecutionRevision{pins[1]}))
	got, err := f.recorder.RevisionPins(t.Context(), f.session, execution)
	testutil.FailErr(t, "read appended pins", err)
	if len(got) != 3 || got[0] != pins[1] || got[1] != pins[1] || got[2] != pins[0] {
		t.Fatalf("pins = %+v", got)
	}
	invalid := pins[0]
	invalid.Role = ""
	if err := f.recorder.AppendRevisionPin(t.Context(), execution, invalid); err == nil {
		t.Fatal("invalid capture appended")
	}
	if err := f.recorder.AppendRevisionPin(t.Context(), "", pins[0]); err == nil {
		t.Fatal("capture appended without an execution")
	}
}

// TestProgramReceiptsPinAWalkAtTheVisitLimit pins as many captures as one
// execution may hold (every traversal visit plus every expanded operand),
// in small appends as a walking owner records them. Full runs seal into
// compressed segments, so rows stay below one segment, the exact conflict
// check reaches sealed pins and one capture more is refused.
func TestProgramReceiptsPinAWalkAtTheVisitLimit(t *testing.T) {
	f := newProgramReceiptFixture(t)
	execution := f.child(t).Invocation().Identity.ExecutionID
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "catalog", err)
	maximum := 0
	for _, limit := range catalog.Language.Limits {
		if limit.ID == "traversal_visits" || limit.ID == "expanded_fields" {
			maximum += int(limit.Maximum)
		}
	}
	base := revisionPinsFixture()[2]
	pin := func(i int) toolcontract.ExecutionRevision {
		p := base
		p.Path = fmt.Sprintf("tree/%06d.txt", i)
		return p
	}
	for i := 0; i < maximum; i += 16 {
		batch := make([]toolcontract.ExecutionRevision, 0, 16)
		for j := i; j < min(i+16, maximum); j++ {
			batch = append(batch, pin(j))
		}
		if err := f.recorder.RecordRevisionPins(t.Context(), execution, i, batch); err != nil {
			t.Fatalf("pins from %d: %v", i, err)
		}
	}
	var rows, segments int
	testutil.FailErr(t, "count rows", f.database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_revision_pins WHERE execution_id=?`, execution).Scan(&rows))
	testutil.FailErr(t, "count segments", f.database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_revision_pin_segments WHERE execution_id=?`, execution).Scan(&segments))
	if rows != maximum%1024 || segments != maximum/1024 {
		t.Fatalf("rows %d segments %d for %d pins", rows, segments, maximum)
	}
	got, err := f.recorder.RevisionPins(t.Context(), f.session, execution)
	testutil.FailErr(t, "read every pin", err)
	if len(got) != maximum || got[0] != pin(0) || got[1500] != pin(1500) || got[maximum-1] != pin(maximum-1) {
		t.Fatalf("read %d pins", len(got))
	}
	testutil.FailErr(t, "resubmit a range across a segment edge", f.recorder.RecordRevisionPins(t.Context(), execution, 1020, got[1020:1030]))
	moved := got[1500]
	moved.ContentBytes++
	if err := f.recorder.RecordRevisionPins(t.Context(), execution, 1500, []toolcontract.ExecutionRevision{moved}); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("sealed capture refreshed: %v", err)
	}
	if err := f.recorder.RecordRevisionPins(t.Context(), execution, maximum, []toolcontract.ExecutionRevision{pin(maximum)}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("pin past the limit: %v", err)
	}
	if err := f.recorder.RecordRevisionPins(t.Context(), execution, maximum+1, []toolcontract.ExecutionRevision{pin(0)}); err == nil || !strings.Contains(err.Error(), "gap") {
		t.Fatalf("gap accepted: %v", err)
	}
	_, err = f.database.ExecContext(t.Context(), `UPDATE invocation_program_revision_pin_segments SET body_sha256=? WHERE execution_id=? AND first_ordinal=1024`, strings.Repeat("0", 64), execution)
	testutil.FailErr(t, "corrupt digest", err)
	if _, err := f.recorder.RevisionPins(t.Context(), f.session, execution); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("corrupt segment read: %v", err)
	}
}
