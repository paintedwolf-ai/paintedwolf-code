package app

import (
	"bytes"
	"fmt"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/textutil"
	"io"
	"strings"
	"testing"
)

type deliveredCommandBytes struct {
	bytes.Buffer
	mappings []toolcontract.StreamSourceMapping
}

func (b *deliveredCommandBytes) RecordSourceMapping(m toolcontract.StreamSourceMapping) error {
	b.mappings = append(b.mappings, m)
	return nil
}

func TestCommandSedDeliveryAuthorizesOnlySeenEditorLines(t *testing.T) {
	var content strings.Builder
	for n := 1; n <= 30; n++ {
		fmt.Fprintf(&content, "line %02d\n", n)
	}
	f := newRevisionFixture(t, map[string]string{"a.txt": content.String()})
	texts, err := textutil.NewOwners(f.revisions, f.recorder)
	testutil.FailErr(t, "text owners", err)
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "memory", err)
	var output deliveredCommandBytes
	result, err := texts.Builtins(textutil.Ports{ToolContext: f.tctx, Memory: memory})["sed"].Invoke(t.Context(),
		commandprogram.OperationRequest{OperationID: "sed", Argv: queryFields("sed", "-n", "10,20p", "a.txt"), Invocation: toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: f.program, ExecutionID: f.execution}}},
		commandprogram.Streams{Input: io.NopCloser(strings.NewReader("")), Output: &output, Error: io.Discard})
	testutil.FailErr(t, "sed read", err)
	if result.Status != 0 || !strings.HasPrefix(output.String(), "line 10\n") || !strings.HasSuffix(output.String(), "line 20\n") || len(output.mappings) == 0 {
		t.Fatalf("output=%q result=%+v mappings=%+v", output.String(), result, output.mappings)
	}
	f.noReadBases(t)
	exhausted, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1, State: 1})
	testutil.FailErr(t, "exhausted optional delivery budget", err)
	for _, m := range output.mappings {
		tools.RememberCommandSourceDelivery(t.Context(), f.tctx, exhausted, m, output.Bytes()[m.OutputStartByte:m.OutputEndByte])
	}
	f.noReadBases(t)
	for _, m := range output.mappings {
		tools.RememberCommandSourceDelivery(t.Context(), f.tctx, memory, m, output.Bytes()[m.OutputStartByte:m.OutputEndByte])
	}
	bases := f.adapter.FreezeAgentReads(f.project.ID, f.session)
	if len(bases) != 1 {
		t.Fatalf("delivered read bases=%+v", bases)
	}
	for _, basis := range bases {
		doc := tools.EditorDocumentText{ReadWhole: basis.Whole, ReadSpans: basis.Spans}
		if !doc.CoversLines(10, 20) || doc.CoversLines(9, 10) || doc.CoversLines(20, 21) || doc.CoversLines(25, 25) {
			t.Fatalf("delivered line coverage=%+v", basis)
		}
	}
	f.tctx.Source.EditorReadBases = tools.NewAgentReadBases(bases)
	_, err = (&native.WriteTool{Boundary: f.revisions.Boundary}).Run(t.Context(), map[string]any{"path": "a.txt", "content": "replacement\n"}, f.tctx)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "EDITOR_DOCUMENT_READ_REQUIRED" {
		t.Fatalf("partial delivery authorized whole-document replacement: %v", err)
	}
	if f.disk(t, "a.txt") != content.String() {
		t.Fatal("rejected write changed disk")
	}

}

func TestCommandDeliveryBareCarriageReturnDoesNotAuthorizeHiddenText(t *testing.T) {
	f := newRevisionFixture(t, map[string]string{"a.txt": "safe\rhidden\n"})
	captured, err := f.revisions.CaptureBytes(t.Context(), f.tctx, "a.txt", 1024)
	testutil.FailErr(t, "capture saved bytes", err)
	pin, ok := captured.Revision.SourcePin()
	if !ok {
		t.Fatal("captured disk revision has no source pin")
	}
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "memory", err)
	mapping := toolcontract.StreamSourceMapping{Source: pin, SourceEndByte: 4, OutputEndByte: 4}
	tools.RememberCommandSourceDelivery(t.Context(), f.tctx, memory, mapping, []byte("safe"))
	f.noReadBases(t)
	_, err = (&native.WriteTool{Boundary: f.revisions.Boundary}).Run(t.Context(), map[string]any{"path": "a.txt", "content": "safe\rchanged\n"}, f.tctx)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "EDITOR_DOCUMENT_READ_REQUIRED" {
		t.Fatalf("hidden text edit=%v", err)
	}
	if f.disk(t, "a.txt") != "safe\rhidden\n" {
		t.Fatal("partial delivery authorized changing hidden bytes")
	}
}
