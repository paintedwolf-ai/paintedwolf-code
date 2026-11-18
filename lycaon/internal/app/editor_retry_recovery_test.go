package app

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/textutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCommandReadResolvesIdenticalRejectedEdit(t *testing.T) {
	f := newRevisionFixture(t, map[string]string{"a.txt": "base\n"})
	ctx := t.Context()
	args := map[string]any{"path": "a.txt", "content": "changed\n"}
	guard := loopguard.NewMemoryDoomLoopGuard()
	for response := range loopguard.DoomLoopMaxSameCodeRejects {
		_, err := f.write.Run(ctx, args, f.tctx)
		reject := toolrejection.AsToolReject(err)
		if reject == nil || reject.Code != "EDITOR_DOCUMENT_READ_REQUIRED" {
			t.Fatalf("missing read refusal: %v", err)
		}
		testutil.FailErr(t, "record read prerequisite", guard.RecordAttempt(ctx, f.session, fmt.Sprint(response), "write", args, &api.InvocationFailure{Code: reject.Code, Details: reject.Data}, false))
	}

	texts, err := textutil.NewOwners(f.revisions, f.recorder)
	testutil.FailErr(t, "text owners", err)
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "memory", err)
	var output deliveredCommandBytes
	_, err = texts.Builtins(textutil.Ports{ToolContext: f.tctx, Memory: memory})["sed"].Invoke(ctx,
		commandprogram.OperationRequest{OperationID: "sed", Argv: queryFields("sed", "-n", "1,$p", "a.txt"), Invocation: toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: f.program, ExecutionID: f.execution}}},
		commandprogram.Streams{Input: io.NopCloser(strings.NewReader("")), Output: &output, Error: io.Discard})
	testutil.FailErr(t, "sed read", err)
	for _, m := range output.mappings {
		tools.RememberCommandSourceDelivery(ctx, f.tctx, memory, m, output.Bytes()[m.OutputStartByte:m.OutputEndByte])
	}
	bases := f.adapter.FreezeAgentReads(f.project.ID, f.session)
	if len(bases) != 1 {
		t.Fatalf("delivered read bases = %+v", bases)
	}

	allowed, _, code, err := guard.Check(ctx, f.session, "after-read", "write", args, true, tools.NewAgentReadBases(bases))
	testutil.FailErr(t, "check retry after read", err)
	if !allowed {
		t.Fatalf("resolved read still blocked with %s", code)
	}
	f.tctx.Source.EditorReadBases = tools.NewAgentReadBases(bases)
	_, err = f.write.Run(ctx, args, f.tctx)
	testutil.FailErr(t, "apply recovered edit", err)
	if got := f.disk(t, "a.txt"); got != "changed\n" {
		t.Fatalf("resolved prerequisite still blocks identical edit: %q", got)
	}
}
