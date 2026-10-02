package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/hitl"
)

// LocalNetworkAsk is a pre-spawn widening of both local-listen and
// loopback-connect. Post-failure stays single-axis: an errno names one operation.
type LocalNetworkAsk struct {
	SecretPermission *hitl.SecretPermission
	SessionID        string
	ParentSessionID  string
	ProjectID        string
	ToolCallID       string
	ProjectDir       string
	ToolName         string
	Command          string
	ListenPorts      []uint16
	ConnectPorts     []uint16
}

// LocalNetworkResult is the combined local-network authority after optional HITL.
type LocalNetworkResult struct {
	SecretApproved bool
	Raised         bool
	Authorized     bool
	Denied         bool
	ListenPorts    []uint16
	ConnectPorts   []uint16
	UserGuidance   string
}

// LocalNetworkGate reviews one invocation that widens both axes.
type LocalNetworkGate interface {
	AwaitCombined(ctx context.Context, in LocalNetworkAsk) (LocalNetworkResult, error)
}
