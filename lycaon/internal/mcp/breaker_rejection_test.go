package mcp

import (
	"errors"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/sony/gobreaker"
	"testing"
)

func TestBreakerRecoveryKeepsOpenAndProbeStatesDistinct(t *testing.T) {
	for _, tc := range []struct {
		cause error
		probe bool
	}{{gobreaker.ErrOpenState, false}, {gobreaker.ErrTooManyRequests, true}} {
		var reject *tools.ToolReject
		if !errors.As(breakerReject("provider", "tool", tc.cause), &reject) || reject.Code != MCPTransportUnavailableCode || reject.Data["mcp_probe_in_flight"] != tc.probe {
			t.Fatalf("wrong breaker state: %+v", reject)
		}
	}
	original := errors.New("unrelated transport error")
	if !errors.Is(breakerReject("provider", "tool", original), original) {
		t.Fatal("unrelated error became breaker state")
	}
}
