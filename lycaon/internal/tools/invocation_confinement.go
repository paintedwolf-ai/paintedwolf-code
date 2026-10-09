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
	if strings.TrimSpace(tctx.Source.WorkerBranchRoot) != "" {
		readDenyPaths = append(readDenyPaths, tctx.Source.WorkerSourceRoots...)
	}
	overlayReadPaths := tctx.Files.SessionReadPaths
	if tctx.Files.PackageExecution != nil {
		readDenyPaths = append(readDenyPaths, tctx.Files.PackageExecution.SensitiveReads...)
		// Package actions use only the read access explicitly requested for this invocation.
		overlayReadPaths = tctx.Files.PackageExecution.ApprovedReadPaths
	}
	return hitl.ActionConfineInputs{
		ProcessControl: tctx.Execution.ProcessControl, HostExecution: tctx.Execution.HostExecution,
		ProjectID:            tctx.Identity.ProjectID,
		Roots:                ConfineRootsForAction(tctx),
		SessionScratchRoot:   tctx.Host.SessionScratchDir,
		OverlayWriteRoots:    overlayWriteRoots,
		PolicyWriteGrants:    tctx.Files.PolicyWriteGrants,
		OverlayReadPaths:     overlayReadPaths,
		ReadDenyPaths:        readDenyPaths,
		ReadRoots:            ConfineReadRootsForAction(tctx),
		SocketGrants:         tctx.Socket.SocketGrants,
		SocksProxyEnv:        tctx.Local.SocksProxyEnv,
		DirectIP:             tctx.Direct.DirectIPRequested,
		DirectIPDeclared:     tctx.Direct.DirectIPDeclared,
		LocalListen:          tctx.Local.LocalListenGranted,
		LocalListenPorts:     tctx.Local.LocalListenPorts,
		LoopbackConnect:      tctx.Local.LoopbackConnectGranted,
		LoopbackConnectPorts: tctx.Local.LoopbackConnectPorts,
	}
}

type SessionListenGrant func(ctx context.Context, sessionID, parentSessionID string) (bool, []uint16)
type SessionLoopbackGrant func(ctx context.Context, sessionID, parentSessionID string) (bool, []uint16)

// SandboxScopeContext attaches session id and scope template tokens for path-scope checks.
func SandboxScopeContext(ctx context.Context, tc ToolContext) context.Context {
	ctx = sandbox.WithSessionID(ctx, tc.Identity.SessionID)
	ctx = sandbox.WithScopeTokens(ctx, sandbox.ScopeTokens{
		Self: tc.Identity.Agent,
		Job:  tc.Identity.WorkerJobID,
	})
	if len(tc.Turn.TurnWritePinGlobs) > 0 {
		pin := sandbox.TurnWritePin{Globs: append([]string(nil), tc.Turn.TurnWritePinGlobs...)}
		for _, root := range tc.Source.Roots {
			if root.ID == tc.Turn.TurnWritePinRootID {
				pin.RootPath = root.Path
				break
			}
		}
		// An unresolved root leaves RootPath empty, which denies all writes.
		ctx = sandbox.WithTurnWritePin(ctx, pin)
	}
	return ctx
}
