package app

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/query"
	"github.com/lycaon/lycaon/internal/tools/native/textutil"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func queryFields(words ...string) commandprogram.Fields {
	out := make(commandprogram.Fields, len(words))
	for i, word := range words {
		out[i] = commandprogram.Field{Value: word}
	}
	return out
}

// requireAcceptedPin asserts the execution pinned exactly the draft's
// accepted revision and seeded no conversational read base.
func requireAcceptedPin(t *testing.T, f *revisionFixture, documentID string, revision int64) {
	t.Helper()
	pins, err := f.recorder.RevisionPins(t.Context(), f.session, f.execution)
	testutil.FailErr(t, "recorded pins", err)
	if len(pins) != 1 || pins[0].Mode != toolcontract.AcceptedDocumentRevision || pins[0].DocumentID != documentID || pins[0].DocumentRevision != revision {
		t.Fatalf("accepted revision pins = %+v", pins)
	}
	f.noReadBases(t)
}

// TestCommandJqReadsTheAcceptedDocument: a value query reads the person's
// unsaved draft, not disk, and pins the accepted revision it read.
func TestCommandJqReadsTheAcceptedDocument(t *testing.T) {
	f := newRevisionFixture(t, map[string]string{"config.json": `{"replicas":1}` + "\n"})
	ctx := t.Context()
	draft := f.personTypes(t, ctx, "config.json", `{"replicas":3}`+"\n")
	queries, err := query.NewOwners(f.revisions, f.recorder, f.write)
	testutil.FailErr(t, "query owners", err)
	paths, err := projectpaths.NewProgramPaths(ctx, f.revisions.Boundary, f.tctx, ".")
	testutil.FailErr(t, "program cwd", err)
	var out bytes.Buffer
	result, err := queries.Invoke(ctx, f.tctx, query.Program{Paths: paths, Barrier: &native.PublicationBarrier{}},
		commandprogram.OperationRequest{OperationID: "jq", Argv: queryFields("jq", ".replicas", "config.json"),
			Invocation: toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: f.program, ExecutionID: f.execution}}},
		commandprogram.Streams{Input: io.NopCloser(strings.NewReader("")), Output: &out, Error: io.Discard})
	testutil.FailErr(t, "jq over draft", err)
	if out.String() != "3\n" || result.Status != 0 {
		t.Fatalf("jq read %q %+v, want the accepted draft", out.String(), result)
	}
	requireAcceptedPin(t, f, draft.ID, draft.Revision)
}

// TestCommandAwkRecordsReadTheAcceptedDocument: a field and record query on
// the shared awk owner reads the draft and pins its accepted revision.
func TestCommandAwkRecordsReadTheAcceptedDocument(t *testing.T) {
	f := newRevisionFixture(t, map[string]string{"load.csv": "api,1\ndb,2\n"})
	ctx := t.Context()
	draft := f.personTypes(t, ctx, "load.csv", "api,1\ndb,2\nweb,4\n")
	texts, err := textutil.NewOwners(f.revisions, f.recorder)
	testutil.FailErr(t, "text owners", err)
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "memory limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "memory", err)
	var out bytes.Buffer
	result, err := texts.Builtins(textutil.Ports{ToolContext: f.tctx, Memory: memory})["awk"].Invoke(ctx,
		commandprogram.OperationRequest{OperationID: "awk", Argv: queryFields("awk", "-F", ",", "{ s += $2 } END { print NR, s }", "load.csv"),
			Invocation: toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: f.program, ExecutionID: f.execution}}},
		commandprogram.Streams{Input: io.NopCloser(strings.NewReader("")), Output: &out, Error: io.Discard})
	testutil.FailErr(t, "awk over draft", err)
	if out.String() != "3 7\n" || result.Status != 0 {
		t.Fatalf("awk read %q %+v, want the accepted draft", out.String(), result)
	}
	if f.disk(t, "load.csv") != "api,1\ndb,2\n" {
		t.Fatal("record query published the draft")
	}
	requireAcceptedPin(t, f, draft.ID, draft.Revision)
}
