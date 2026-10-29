package app

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
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

// requireDiskPin confirms capture alone grants no conversational read basis.
func requireDiskPin(t *testing.T, f *revisionFixture) {
	t.Helper()
	pins, err := f.recorder.RevisionPins(t.Context(), f.session, f.execution)
	testutil.FailErr(t, "recorded pins", err)
	if len(pins) != 1 || pins[0].Mode != toolcontract.PublishedDiskRevision {
		t.Fatalf("disk pins=%+v", pins)
	}
	f.noReadBases(t)
}

// TestCommandJqReadsSavedDisk: a value query reads published disk and leaves human drafts untouched.
func TestCommandJqReadsSavedDisk(t *testing.T) {
	f := newRevisionFixture(t, map[string]string{"config.json": `{"replicas":1}` + "\n"})
	ctx := t.Context()
	f.personTypes(t, ctx, "config.json", `{"replicas":3}`+"\n")
	queries, err := query.NewOwners(f.revisions, f.recorder, f.write)
	testutil.FailErr(t, "query owners", err)
	paths, err := projectpaths.NewProgramPaths(ctx, f.revisions.Boundary, f.tctx, ".")
	testutil.FailErr(t, "program cwd", err)
	var out bytes.Buffer
	result, err := queries.Invoke(ctx, f.tctx, query.Program{Paths: paths},
		commandprogram.OperationRequest{OperationID: "jq", Argv: queryFields("jq", ".replicas", "config.json"),
			Invocation: toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: f.program, ExecutionID: f.execution}}},
		commandprogram.Streams{Input: io.NopCloser(strings.NewReader("")), Output: &out, Error: io.Discard})
	testutil.FailErr(t, "jq over draft", err)
	if out.String() != "1\n" || result.Status != 0 {
		t.Fatalf("jq read %q %+v, want saved disk", out.String(), result)
	}
	requireDiskPin(t, f)
}

// TestCommandAwkRecordsReadSavedDisk: a field and record query on
// the shared awk owner reads disk and pins the published revision.
func TestCommandAwkRecordsReadSavedDisk(t *testing.T) {
	f := newRevisionFixture(t, map[string]string{"load.csv": "api,1\ndb,2\n"})
	ctx := t.Context()
	f.personTypes(t, ctx, "load.csv", "api,1\ndb,2\nweb,4\n")
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
	if out.String() != "2 3\n" || result.Status != 0 {
		t.Fatalf("awk read %q %+v, want saved disk", out.String(), result)
	}
	if f.disk(t, "load.csv") != "api,1\ndb,2\n" {
		t.Fatal("record query published the draft")
	}
	requireDiskPin(t, f)
}
