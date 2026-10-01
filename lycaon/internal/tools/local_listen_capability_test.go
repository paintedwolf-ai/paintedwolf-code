package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// TestParseLocalListenShapes accepts {} and narrowed ports; rejects malformed shapes.
func TestParseLocalListenShapes(t *testing.T) {
	t.Parallel()
	req, reject := ParseCapabilityRequest(map[string]any{
		"capability_request": map[string]any{"local_listen": map[string]any{}},
	})
	if reject != nil || req == nil || req.LocalListen == nil || len(req.LocalListen.Ports) != 0 {
		t.Fatalf("empty ask: req=%+v reject=%+v", req, reject)
	}

	req, reject = ParseCapabilityRequest(map[string]any{
		"capability_request": map[string]any{"local_listen": map[string]any{
			"ports": []any{float64(9000), float64(8000), float64(8000)},
		}},
	})
	if reject != nil || req.LocalListen == nil {
		t.Fatalf("port ask rejected: %+v", reject)
	}
	if p := req.LocalListen.Ports; len(p) != 2 || p[0] != 8000 || p[1] != 9000 {
		t.Fatalf("ports not deduped+sorted: %v", p)
	}

	for name, listen := range map[string]any{
		"non-object":    "yes",
		"unknown-field": map[string]any{"port": float64(80)},
		"port-zero":     map[string]any{"ports": []any{float64(0)}},
		"port-high":     map[string]any{"ports": []any{float64(70000)}},
		"port-frac":     map[string]any{"ports": []any{float64(80.5)}},
		"too-many": map[string]any{"ports": []any{
			float64(1), float64(2), float64(3), float64(4), float64(5),
			float64(6), float64(7), float64(8), float64(9),
		}},
	} {
		if _, reject := ParseCapabilityRequest(map[string]any{
			"capability_request": map[string]any{"local_listen": listen},
		}); reject == nil || reject.Code != "SANDBOX_LOCAL_LISTEN_REQUEST_INVALID" {
			t.Fatalf("%s: expected SANDBOX_LOCAL_LISTEN_REQUEST_INVALID, got %+v", name, reject)
		}
	}
}

func TestAxisPortsCovered(t *testing.T) {
	t.Parallel()
	cases := []struct {
		held, asked []uint16
		want        bool
	}{
		{nil, []uint16{8000}, true},
		{[]uint16{8000}, nil, false},
		{[]uint16{8000, 9000}, []uint16{9000}, true},
		{[]uint16{8000}, []uint16{9000}, false},
	}
	for _, tc := range cases {
		if got := axisPortsCovered(tc.held, tc.asked); got != tc.want {
			t.Fatalf("covered(%v, %v)=%v want %v", tc.held, tc.asked, got, tc.want)
		}
	}
}

func TestLocalListenRequestRequiresApprovalBroker(t *testing.T) {
	t.Parallel()
	executor := NewDefaultToolExecutor(nil, nil, "")
	args := map[string]any{"capability_request": map[string]any{
		"local_listen": map[string]any{"ports": []any{float64(8000)}},
	}}
	tc := ToolContext{Invocation: Invocation{Contract: toolcontract.Contract{
		Capabilities: toolcontract.CapabilityLocalListen,
	}}}

	result, err := executor.preflightLocalListenCapability(context.Background(), "command", args, tc)
	var reject *ToolReject
	if result != nil || !errors.As(err, &reject) || reject.Code != "SANDBOX_APPROVAL_UNAVAILABLE" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}
