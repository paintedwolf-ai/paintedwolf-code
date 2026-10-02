package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/hitl"
)

// LocalListenAsk is the input for a structured pre-spawn listener approval.
type LocalListenAsk struct {
	SecretPermission *hitl.SecretPermission
	SessionID        string
	ParentSessionID  string
	ProjectID        string
	ToolCallID       string
	ProjectDir       string
	// ToolName is the argv-running tool.
	ToolName string
	Command  string
	// Ports narrows the proposal to exact local ports; empty asks for any.
	Ports []uint16
}

// LocalListenResult is the gate outcome after optional HITL.
type LocalListenResult struct {
	SecretApproved bool
	// Raised reports whether an approval was awaited.
	Raised bool
	// Authorized reports whether listener authority covers the ask.
	Authorized bool
	// Denied reports a rejected or expired approval.
	Denied bool
	// Ports is the covering narrowing; empty with Authorized means any local port.
	Ports []uint16
	// UserGuidance is explicit composer direction attached to a denial.
	UserGuidance string
}

// LocalListenGate reviews and exposes chat listener authority.
type LocalListenGate interface {
	Await(ctx context.Context, in LocalListenAsk) (LocalListenResult, error)
	// SessionListenGrant reports the live chat lease.
	SessionListenGrant(ctx context.Context, sessionID, parentSessionID string) (bool, []uint16)
}
