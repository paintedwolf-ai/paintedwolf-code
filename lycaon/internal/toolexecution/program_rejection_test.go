package toolexecution

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestNativeRejectionPreservesActualOwnerCodeOperandAndNestedLocation(t *testing.T) {
	invocation, _ := frameFixture(t)
	invocation.Span = toolcontract.SourceSpan{StartByte: 4, EndByte: 12, Line: 2, Column: 3}
	definition := nativeDefinition(t, "read")
	bound, err := toolcontract.BindOperation(definition, invocation, "cat", definition.Facts().Commands[0].Effects[0].Mode, nil)
	testutil.FailErr(t, "bind actual operation", err)
	rejected := &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"path": "missing-fixture", "expected_form": "cat -- <path>"}}
	wrapped := errors.Join(errors.New("diagnostic context"), rejected)
	completeNativeRejection(bound, wrapped)
	if !errors.Is(wrapped, rejected) || rejected.OwnerRef != definition.Facts().Contract.Owner || rejected.Code != "TOOL_ARGS_INVALID" || rejected.Data["path"] != "missing-fixture" || rejected.Data["expected_form"] != "cat -- <path>" || rejected.Data["source_line"] != 2 {
		t.Fatalf("owner attribution lost actual facts: %+v", rejected)
	}
	nested := &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", OwnerRef: "filesystem", Data: map[string]any{"source_line": 8, "source_start_byte": 30}}
	completeNativeRejection(bound, nested)
	_, mixed := nested.Data["source_end_byte"]
	if mixed || nested.Data["source_line"] != 8 || nested.Data["source_start_byte"] != 30 {
		t.Fatal("operation attribution overwrote a more precise owner location")
	}
	completeNativeRejection(toolcontract.BoundOperation{}, rejected)
	completeNativeRejection(bound, errors.New("ordinary failure"))
}
