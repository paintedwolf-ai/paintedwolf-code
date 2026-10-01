package tools

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
)

// SessionWriteRootOverlay returns the session tree’s approved write paths.
type SessionWriteRootOverlay func(ctx context.Context, sessionID, parentSessionID string) []string

// WriteRootPreflight resolves one explicit root before process spawn.
type WriteRootPreflight func(ctx context.Context, tool string, args map[string]any, tc ToolContext, root string) (authorized, denied bool, guidance string, err error)

func (e *DefaultToolExecutor) SetWriteRootPreflight(fn WriteRootPreflight) {
	if e != nil {
		e.writeRootPreflight = fn
	}
}

// ReadPathPreflight resolves one explicit protected read path before spawn.
type ReadPathPreflight func(ctx context.Context, tool string, args map[string]any, tc ToolContext, path string) (authorized, denied bool, guidance string, err error)

func (e *DefaultToolExecutor) SetReadPathPreflight(fn ReadPathPreflight) {
	if e != nil {
		e.readPathPreflight = fn
	}
}

// SessionReadPathOverlay returns the session tree's live protected-read leases.
type SessionReadPathOverlay func(ctx context.Context, sessionID, parentSessionID string) []string

// SetSessionReadPathOverlay supplies read grants to review and execution.
func (e *DefaultToolExecutor) SetSessionReadPathOverlay(fn SessionReadPathOverlay) {
	if e != nil {
		e.sessionReadOverlay = fn
	}
}

// SetSessionWriteRootOverlay supplies write grants to review and execution.
func (e *DefaultToolExecutor) SetSessionWriteRootOverlay(fn SessionWriteRootOverlay) {
	if e != nil {
		e.sessionOverlay = fn
	}
}

// SessionListenGrant reports the session tree's live local-listener lease.
type SessionListenGrant func(ctx context.Context, sessionID, parentSessionID string) (bool, []uint16)

// SessionLoopbackGrant reports chat authority for outbound local connections.
type SessionLoopbackGrant func(ctx context.Context, sessionID, parentSessionID string) (bool, []uint16)

// SetSessionListenGrant supplies chat listener authority to confined spawns.
func (e *DefaultToolExecutor) SetSessionListenGrant(fn SessionListenGrant) {
	if e != nil {
		e.sessionListenGrant = fn
	}
}

// SetSessionLoopbackGrant wires the chat's local client lease source.
func (e *DefaultToolExecutor) SetSessionLoopbackGrant(fn SessionLoopbackGrant) {
	if e != nil {
		e.sessionLoopbackGrant = fn
	}
}

// Copy live listener authority before constructing the spawn boundary.
func (e *DefaultToolExecutor) applySessionListenGrant(ctx context.Context, tctx *ToolContext) {
	if e == nil || e.sessionListenGrant == nil || tctx == nil || tctx.PackageExecution != nil {
		return
	}
	granted, ports := e.sessionListenGrant(ctx, tctx.SessionID, tctx.ParentSessionID)
	if !granted {
		return
	}
	tctx.LocalListenGranted = true
	tctx.LocalListenPorts = ports
}

func (e *DefaultToolExecutor) applySessionLoopbackGrant(ctx context.Context, tctx *ToolContext) {
	if e == nil || e.sessionLoopbackGrant == nil || tctx == nil || tctx.PackageExecution != nil {
		return
	}
	granted, ports := e.sessionLoopbackGrant(ctx, tctx.SessionID, tctx.ParentSessionID)
	if !granted {
		return
	}
	tctx.LoopbackConnectGranted = true
	tctx.LoopbackConnectPorts = ports
}

func (e *DefaultToolExecutor) overlayWriteRoots(ctx context.Context, tctx ToolContext) []string {
	var roots []string
	if planned, ok := ctx.Value(capabilityReviewRootsKey{}).([]string); ok {
		roots = append(roots, planned...)
	}
	if e != nil && e.sessionOverlay != nil {
		roots = append(roots, e.sessionOverlay(ctx, tctx.SessionID, tctx.ParentSessionID)...)
	}
	roots = append(roots, tctx.RealizationWriteRoots...)
	return uniqueRoots(roots)
}

func uniqueRoots(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, root := range in {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	return out
}

// ActionConfineInputsForContext maps an invocation context onto the one
// per-action confine construction input (hitl.ActionConfineRequest).
func ActionConfineInputsForContext(tctx ToolContext, overlayWriteRoots []string) hitl.ActionConfineInputs {
	var readDenyPaths []string
	if strings.TrimSpace(tctx.WorkerBranchRoot) != "" {
		readDenyPaths = append(readDenyPaths, tctx.WorkerSourceRoots...)
	}
	overlayReadPaths := tctx.SessionReadPaths
	if tctx.PackageExecution != nil {
		readDenyPaths = append(readDenyPaths, tctx.PackageExecution.SensitiveReads...)
		// Package actions use only the read access explicitly requested for this invocation.
		overlayReadPaths = tctx.PackageExecution.ApprovedReadPaths
	}
	return hitl.ActionConfineInputs{
		ProcessControl: tctx.ProcessControl, HostExecution: tctx.HostExecution,
		ProjectID:            tctx.ProjectID,
		Roots:                ConfineRootsForAction(tctx),
		SessionScratchRoot:   tctx.SessionScratchDir,
		OverlayWriteRoots:    overlayWriteRoots,
		PolicyWriteGrants:    tctx.PolicyWriteGrants,
		OverlayReadPaths:     overlayReadPaths,
		ReadDenyPaths:        readDenyPaths,
		ReadRoots:            ConfineReadRootsForAction(tctx),
		SocketGrants:         tctx.SocketGrants,
		SocksProxyEnv:        tctx.SocksProxyEnv,
		DirectIP:             tctx.DirectIPRequested,
		DirectIPDeclared:     tctx.DirectIPDeclared,
		LocalListen:          tctx.LocalListenGranted,
		LocalListenPorts:     tctx.LocalListenPorts,
		LoopbackConnect:      tctx.LoopbackConnectGranted,
		LoopbackConnectPorts: tctx.LoopbackConnectPorts,
	}
}

// actionConfineRequest is the executor's gate-side view of the request the
// spawn path will apply for this action.
func (e *DefaultToolExecutor) actionConfineRequest(ctx context.Context, tctx ToolContext) confine.Request {
	return hitl.ActionConfineRequest(ActionConfineInputsForContext(tctx, e.overlayWriteRoots(ctx, tctx)))
}
