package command

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestNativeLoopbackRequestOnlyTargetsForeignPorts(t *testing.T) {
	contract, ok := toolcontract.Lookup("http_request")
	if !ok || !contract.Supports(toolcontract.CapabilityLoopbackConnect) {
		t.Fatal("http_request must accept loopback capability")
	}
	run := &programRun{tc: tools.ToolContext{Local: tools.InvocationLocal{OwnedLoopbackPorts: []uint16{41001}}}}
	args := run.nativeArguments(map[string]any{}, contract, []uint16{41001})
	if _, present := args["capability_request"]; present {
		t.Fatalf("owned listener was turned into a consented request: %+v", args)
	}
	args = run.nativeArguments(map[string]any{}, contract, []uint16{41001, 41002})
	want := map[string]any{"loopback_connect": map[string]any{"ports": []any{41002}}}
	if !reflect.DeepEqual(args["capability_request"], want) {
		t.Fatalf("foreign listener request = %+v, want %+v", args["capability_request"], want)
	}
}
