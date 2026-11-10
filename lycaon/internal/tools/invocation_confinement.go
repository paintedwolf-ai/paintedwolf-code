package tools

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/sandbox"
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
	writeLeg := strings.TrimSpace(tctx.Source.WorkerBranchRoot) != ""
	if writeLeg || strings.TrimSpace(tctx.Identity.WorkerJobID) != "" {
		readDenyPaths = append(readDenyPaths, confine.WorkerReadDenyRoots(writeLeg)...)
	}
	if writeLeg {
		readDenyPaths = append(readDenyPaths, tctx.Source.WorkerSourceRoots...)
	}
	overlayReadPaths := append([]string(nil), tctx.Files.SessionReadPaths...)
	protectedWrites := append([]confine.ProtectedPathGrant(nil), tctx.Files.ProtectedWriteGrants...)
	for _, access := range tctx.Files.ApprovedFileAccess {
		path := strings.TrimSpace(access.Path)
		if path == "" {
			continue
		}
		if access.Write {
			protectedWrites = append(protectedWrites, confine.NewProtectedPathGrant(path))
		}
		if tctx.Files.PackageExecution == nil {
			overlayReadPaths = append(overlayReadPaths, path)
		}
	}
	if len(overlayReadPaths) > 0 {
		overlayReadPaths = UniqueRoots(overlayReadPaths)
	}
	if tctx.Files.PackageExecution != nil {
		readDenyPaths = append(readDenyPaths, tctx.Files.PackageExecution.SensitiveReads...)
		// Package actions use only the read access explicitly requested for this invocation.
		overlayReadPaths = tctx.Files.PackageExecution.ApprovedReadPaths
	}
	return hitl.ActionConfineInputs{
		ProcessControl: tctx.Execution.ProcessControl, CredentialStore: tctx.Execution.CredentialStore, HostExecution: tctx.Execution.HostExecution,
		ProjectID:            tctx.Identity.ProjectID,
		Roots:                ConfineRootsForAction(tctx),
		SessionScratchRoot:   tctx.Host.SessionScratchDir,
		SessionOutputRoot:    tctx.Host.SessionOutputDir,
		OverlayWriteRoots:    overlayWriteRoots,
		ProtectedWriteGrants: protectedWrites,
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
	ctx = sandbox.WithSessionDirs(ctx, tc.Host.SessionScratchDir, tc.Host.SessionOutputDir)
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
