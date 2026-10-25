package app

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native/literal"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type mappedOutput struct {
	bytes.Buffer
	mappings []toolcontract.StreamSourceMapping
}

func (m *mappedOutput) RecordSourceMapping(mapping toolcontract.StreamSourceMapping) error {
	m.mappings = append(m.mappings, mapping)
	return nil
}

// TestCommandCatServesTheAcceptedDocumentNewerThanDisk: a native read of a
// path with an unsaved draft returns the draft, encoded exactly as its save
// would write it, records that accepted revision in the program receipt, and
// leaves conversational read bases untouched.
func TestCommandCatServesTheAcceptedDocumentNewerThanDisk(t *testing.T) {
	f := newRevisionFixture(t, map[string]string{"notes.txt": "disk line\r\nsecond\r\n"})
	ctx := t.Context()
	opened, err := f.service.Open(ctx, f.project, "notes.txt", f.rootID, "", "window", nil)
	testutil.FailErr(t, "open notes", err)
	draft, err := f.service.ReplaceSnapshot(ctx, opened.ID, f.project.ID, editordoc.SnapshotReplacement{DocumentCommand: editordoc.DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: opened.Revision}, Content: "draft line\nsecond\nthird", EOL: "crlf"})
	testutil.FailErr(t, "type draft", err)
	owners, err := literal.NewOwners(f.revisions, f.recorder, literal.DomainOwners{})
	testutil.FailErr(t, "literal owners", err)
	paths, err := projectpaths.NewProgramPaths(ctx, f.revisions.Boundary, f.tctx, ".")
	testutil.FailErr(t, "program cwd", err)
	run := func(argv ...string) (string, toolcontract.ProducerResult, []toolcontract.StreamSourceMapping) {
		t.Helper()
		fields := make(commandprogram.Fields, len(argv))
		for i, word := range argv {
			fields[i] = commandprogram.Field{Value: word}
		}
		request := commandprogram.OperationRequest{OperationID: argv[0], Argv: fields,
			Invocation: toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: f.program, ExecutionID: f.execution}}}
		var out mappedOutput
		result, err := owners.Invoke(ctx, f.tctx, paths, request, commandprogram.Streams{Input: io.NopCloser(strings.NewReader("")), Output: &out, Error: io.Discard})
		testutil.FailErr(t, strings.Join(argv, " "), err)
		return out.String(), result, out.mappings
	}

	stdout, result, mappings := run("cat", "notes.txt")
	want := "draft line\r\nsecond\r\nthird"
	if stdout != want || result.Status != 0 || !result.PublicationEligible {
		t.Fatalf("cat served %q (%+v), want the accepted draft", stdout, result)
	}
	if disk := f.disk(t, "notes.txt"); disk != "disk line\r\nsecond\r\n" {
		t.Fatalf("native read published the draft: %q", disk)
	}
	pins, err := f.recorder.RevisionPins(ctx, f.session, f.execution)
	testutil.FailErr(t, "read recorded pins", err)
	if len(pins) != 1 {
		t.Fatalf("pins = %+v", pins)
	}
	pin := pins[0]
	if pin.Mode != toolcontract.AcceptedDocumentRevision || pin.DocumentID != draft.ID || pin.DocumentRevision != draft.Revision ||
		pin.Representation != toolcontract.OriginalEncodedBytes || pin.ContentSHA256 != textfile.SHA256([]byte(want)) || pin.ContentBytes != int64(len(want)) ||
		pin.DiskSHA256 != textfile.SHA256([]byte("disk line\r\nsecond\r\n")) || pin.Path != "notes.txt" {
		t.Fatalf("accepted revision pin = %+v", pin)
	}
	source, _ := pin.SourcePin()
	if len(mappings) != 1 || mappings[0].Source != source || mappings[0].SourceStartByte != 0 || mappings[0].SourceEndByte != int64(len(want)) {
		t.Fatalf("draft mappings = %+v", mappings)
	}
	f.noReadBases(t)

	counted, _, _ := run("wc", "-l", "notes.txt")
	if counted != "2 notes.txt\n" {
		t.Fatalf("wc counted %q over the captured draft", counted)
	}
	digest, _, _ := run("sha256sum", "notes.txt")
	if digest != textfile.SHA256([]byte(want))+"  notes.txt\n" {
		t.Fatalf("checksum %q is not the captured representation", digest)
	}
	pins, err = f.recorder.RevisionPins(ctx, f.session, f.execution)
	testutil.FailErr(t, "read pins after later reads", err)
	if len(pins) != 3 || pins[1] != pin || pins[2] != pin {
		t.Fatalf("each run appends its capture: %+v", pins)
	}
}
