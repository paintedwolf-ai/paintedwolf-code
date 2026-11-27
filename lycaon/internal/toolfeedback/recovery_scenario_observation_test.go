package toolfeedback

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func rejectScenarioObservation(
	t *testing.T,
	bp *BlockPlane,
	ctx context.Context,
	tool string,
	vars map[string]any,
	code string,
	data map[string]any,
) error {
	t.Helper()
	tr := &toolrejection.ToolReject{Code: code, Data: data}
	opName, _ := vars["native_operation"].(string)
	opName = strings.TrimSpace(opName)
	if opName == "" {
		return bp.RejectObservation(ctx, tool, "fixture", nil, tr)
	}
	contract, ok := toolcontract.Lookup(opName)
	if !ok {
		t.Fatalf("unknown scenario native_operation %q", opName)
	}
	def, err := toolcontract.ToolOperation(opName, json.RawMessage(`{"type":"object"}`), contract)
	testutil.FailErr(t, "freeze scenario operation "+opName, err)
	cmd := def.Facts().Commands[0]
	mode := ""
	if len(cmd.Effects) > 0 {
		mode = cmd.Effects[0].Mode
	}
	inv := toolcontract.OperationInvocation{
		Kind:          toolcontract.ProgramChildInvocation,
		ActorID:       "fixture-person",
		SessionID:     "fixture-session",
		Identity:      toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "node", ExecutionID: opName + "-child"},
		Scope:         toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope},
		CatalogDigest: toolcontract.LanguageCatalogDigest(),
		SourceUnitID:  "submitted",
		Span:          toolcontract.SourceSpan{EndByte: 8, Line: 1, Column: 1},
	}
	bound, err := toolcontract.BindOperation(def, inv, cmd.Name, mode, nil)
	testutil.FailErr(t, "bind scenario operation "+opName, err)
	return bp.RejectNativeObservation(ctx, tool, "fixture", map[string]any{"command": cmd.Name}, bound, tr)
}
