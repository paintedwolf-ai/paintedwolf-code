// Package sandbox defines path and tool boundary enforcement for agent safety.
package sandbox

import "context"

// ExecMode controls whether an agent profile may trigger host subprocess execution.
type ExecMode string

const (
	ExecModeNone        ExecMode = "none"
	ExecModeAllowlisted ExecMode = "allowlisted"
)

// ToolAccess is the workflow-declared breadth of an agent's tool surface.
type ToolAccess string

const (
	// ToolAccessProfile uses the agent's named tool-profile allowlist.
	ToolAccessProfile ToolAccess = "profile"
	// ToolAccessAll permits every runtime-registered tool except profile denies.
	ToolAccessAll ToolAccess = "all"
)

// PathOp is a filesystem operation subject to sandbox checks.
type PathOp string

const (
	PathOpRead  PathOp = "read"
	PathOpWrite PathOp = "write"
)

// SandboxBoundary enforces path jail and tool allowlists.
type SandboxBoundary interface {
	AssertPathAllowed(ctx context.Context, projectDir, relPath string, op PathOp) error
	AssertReadScope(ctx context.Context, projectDir, relPath, profileID string) error
	AssertWriteScope(ctx context.Context, projectDir, relPath, profileID string) error
	AssertToolAllowed(ctx context.Context, profileID, toolName string, access ToolAccess) error
	ExecMode(ctx context.Context, profileID string) ExecMode
	// ToolDeferred reports whether the profile allows toolName but keeps its
	// schema out of the upfront LLM surface (loaded on demand via request_tools).
	ToolDeferred(profileID, toolName string, access ToolAccess) bool
}
