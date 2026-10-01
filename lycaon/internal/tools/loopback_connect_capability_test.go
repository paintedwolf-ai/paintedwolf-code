package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestParseLoopbackConnectShapes(t *testing.T) {
	t.Parallel()
	req, reject := ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{
		"loopback_connect": map[string]any{},
	}})
	if reject != nil || req == nil || req.LoopbackConnect == nil || len(req.LoopbackConnect.Ports) != 0 {
		t.Fatalf("empty ask: req=%+v reject=%+v", req, reject)
	}
	req, reject = ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{
		"local_listen":     map[string]any{"ports": []any{float64(8123)}},
		"loopback_connect": map[string]any{"ports": []any{float64(8123), float64(8123)}},
	}})
	if reject != nil || req.LocalListen == nil || req.LoopbackConnect == nil ||
		len(req.LoopbackConnect.Ports) != 1 || req.LoopbackConnect.Ports[0] != 8123 {
		t.Fatalf("composed ask: req=%+v reject=%+v", req, reject)
	}
	for name, raw := range map[string]any{
		"non-object": "yes", "unknown": map[string]any{"host": "localhost"},
		"empty-ports":  map[string]any{"ports": []any{}},
		"invalid-port": map[string]any{"ports": []any{float64(0)}},
	} {
		_, got := ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{
			"loopback_connect": raw,
		}})
		if got == nil || got.Code != "SANDBOX_LOOPBACK_CONNECT_REQUEST_INVALID" {
			t.Fatalf("%s: reject=%+v", name, got)
		}
	}
}

func TestLoopbackConnectRequestRequiresApprovalBroker(t *testing.T) {
	t.Parallel()
	executor := NewDefaultToolExecutor(nil, nil, "")
	args := map[string]any{"capability_request": map[string]any{
		"loopback_connect": map[string]any{"ports": []any{float64(8000)}},
	}}
	tc := ToolContext{Invocation: Invocation{Contract: toolcontract.Contract{
		Capabilities: toolcontract.CapabilityLoopbackConnect,
	}}}

	result, err := executor.preflightLoopbackConnectCapability(context.Background(), "command", args, tc)
	var reject *ToolReject
	if result != nil || !errors.As(err, &reject) || reject.Code != "SANDBOX_APPROVAL_UNAVAILABLE" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}
