package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/hitl"
)

// LoopbackConnectAsk is the input for a chat-scoped approval to connect to a
// TCP or UDP service on this machine.
type LoopbackConnectAsk struct {
	SecretPermission *hitl.SecretPermission
	SessionID        string
	ParentSessionID  string
	ProjectID        string
	ToolCallID       string
	ProjectDir       string
	ToolName         string
	Command          string
	// Ports narrows local destination ports; empty asks for any.
	Ports []uint16
}

// LoopbackConnectResult is the resolved local client authority.
type LoopbackConnectResult struct {
	SecretApproved bool
	// SecretAttestationID names the presence that released person-held values.
	SecretAttestationID string
	Raised              bool
	Authorized          bool
	Denied              bool
	Ports               []uint16
	UserGuidance        string
}

// LoopbackConnectGate raises a local-service checkpoint and exposes its chat lease.
type LoopbackConnectGate interface {
	Await(context.Context, LoopbackConnectAsk) (LoopbackConnectResult, error)
	SessionLoopbackGrant(context.Context, string, string) (bool, []uint16)
}
