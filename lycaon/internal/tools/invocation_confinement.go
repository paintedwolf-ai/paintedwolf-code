package tools

import (
	"github.com/lycaon/lycaon/internal/sandbox"

	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/hitl"
)

func UniqueRoots(in []string) []string {
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

type SessionListenGrant func(ctx context.Context, sessionID, parentSessionID string) (bool, []uint16)
type SessionLoopbackGrant func(ctx context.Context, sessionID, parentSessionID string) (bool, []uint16)

// SandboxScopeContext attaches session id and scope template tokens for path-scope checks.
func SandboxScopeContext(ctx context.Context, tc ToolContext) context.Context {
	ctx = sandbox.WithSessionID(ctx, tc.SessionID)
	ctx = sandbox.WithScopeTokens(ctx, sandbox.ScopeTokens{
		Self: tc.Agent,
		Job:  tc.WorkerJobID,
	})
	if len(tc.TurnWritePinGlobs) > 0 {
		pin := sandbox.TurnWritePin{Globs: append([]string(nil), tc.TurnWritePinGlobs...)}
		for _, root := range tc.Roots {
			if root.ID == tc.TurnWritePinRootID {
				pin.RootPath = root.Path
				break
			}
		}
		// An unresolved root leaves RootPath empty, which denies all writes.
		ctx = sandbox.WithTurnWritePin(ctx, pin)
	}
	return ctx
}
