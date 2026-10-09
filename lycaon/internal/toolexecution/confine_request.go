package toolexecution

import (
	"context"
	"github.com/lycaon/lycaon/internal/tools"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
)

// SessionWriteRootOverlay returns the session tree’s approved write paths.
type SessionWriteRootOverlay func(ctx context.Context, sessionID, parentSessionID string) []string

// WriteRootPreflight resolves one explicit root before process spawn.
type WriteRootPreflight func(ctx context.Context, tool string, args map[string]any, tc tools.ToolContext, root string) (authorized, denied bool, guidance string, err error)

func (e *Boundary) SetWriteRootPreflight(fn WriteRootPreflight) {
	if e != nil {
		e.writeRootPreflight = fn
	}
}

// ReadPathPreflight resolves one explicit protected read path before spawn.
type ReadPathPreflight func(ctx context.Context, tool string, args map[string]any, tc tools.ToolContext, path string) (authorized, denied bool, guidance string, err error)

func (e *Boundary) SetReadPathPreflight(fn ReadPathPreflight) {
	if e != nil {
		e.readPathPreflight = fn
	}
}

// SessionReadPathOverlay returns the session tree's live protected-read leases.
type SessionReadPathOverlay func(ctx context.Context, sessionID, parentSessionID string) []string

// SetSessionReadPathOverlay supplies read grants to review and execution.
func (e *Boundary) SetSessionReadPathOverlay(fn SessionReadPathOverlay) {
	if e != nil {
		e.sessionReadOverlay = fn
	}
}

// SetSessionWriteRootOverlay supplies write grants to review and execution.
func (e *Boundary) SetSessionWriteRootOverlay(fn SessionWriteRootOverlay) {
	if e != nil {
		e.sessionOverlay = fn
	}
}

// SessionListenGrant reports the session tree's live local-listener lease.

// SessionLoopbackGrant reports chat authority for outbound local connections.

// SetSessionListenGrant supplies chat listener authority to confined spawns.
func (e *Boundary) SetSessionListenGrant(fn tools.SessionListenGrant) {
	if e != nil {
		e.sessionListenGrant = fn
	}
}

// SetSessionLoopbackGrant wires the chat's local client lease source.
func (e *Boundary) SetSessionLoopbackGrant(fn tools.SessionLoopbackGrant) {
	if e != nil {
		e.sessionLoopbackGrant = fn
	}
}

// Copy live listener authority before constructing the spawn boundary.
func (e *Boundary) applySessionListenGrant(ctx context.Context, tctx *tools.ToolContext) {
	if e == nil || e.sessionListenGrant == nil || tctx == nil || tctx.Files.PackageExecution != nil {
		return
	}
	granted, ports := e.sessionListenGrant(ctx, tctx.Identity.SessionID, tctx.Identity.ParentSessionID)
	if !granted {
		return
	}
	tctx.Local.LocalListenGranted = true
	tctx.Local.LocalListenPorts = ports
}

func (e *Boundary) applySessionLoopbackGrant(ctx context.Context, tctx *tools.ToolContext) {
	if e == nil || e.sessionLoopbackGrant == nil || tctx == nil || tctx.Files.PackageExecution != nil {
		return
	}
	granted, ports := e.sessionLoopbackGrant(ctx, tctx.Identity.SessionID, tctx.Identity.ParentSessionID)
	if !granted {
		return
	}
	tctx.Local.LoopbackConnectGranted = true
	tctx.Local.LoopbackConnectPorts = ports
}

func (e *Boundary) overlayWriteRoots(ctx context.Context, tctx tools.ToolContext) []string {
	var roots []string
	if planned, ok := ctx.Value(capabilityReviewRootsKey{}).([]string); ok {
		roots = append(roots, planned...)
	}
	if e != nil && e.sessionOverlay != nil {
		roots = append(roots, e.sessionOverlay(ctx, tctx.Identity.SessionID, tctx.Identity.ParentSessionID)...)
	}
	roots = append(roots, tctx.Host.RealizationWriteRoots...)
	return tools.UniqueRoots(roots)
}

// ActionConfineInputsForContext maps an invocation context onto the one
// per-action confine construction input (hitl.ActionConfineRequest).

// actionConfineRequest is the executor's gate-side view of the request the
// spawn path will apply for this action.
func (e *Boundary) actionConfineRequest(ctx context.Context, tctx tools.ToolContext) confine.Request {
	return hitl.ActionConfineRequest(tools.ActionConfineInputsForContext(tctx, e.overlayWriteRoots(ctx, tctx)))
}
