package api

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/capabilitygrants"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/socketref"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Harness socket cards use the actual reference owner when commands require it.
// A pathname-only layout card never supplies native retained-object authority.
func harnessSocketGrant(ctx context.Context, approved, resolved string) (confine.SocketGrant, error) {
	if approved == "" || socketref.Platform().Command != socketref.RetainedObject {
		return confine.SocketGrant{ApprovedPath: approved, ResolvedPath: resolved, Binding: socketref.Binding{Enforcement: socketref.Platform().Command}}, nil
	}
	actual, err := confine.ResolveSocketRequest(approved)
	if err != nil {
		return confine.SocketGrant{}, err
	}
	if resolved != "" && resolved != actual.ResolvedPath {
		return confine.SocketGrant{}, &confine.SocketResolveError{Code: confine.SocketResolveChanged, Path: approved}
	}
	return confine.CaptureSocketGrant(ctx, actual)
}

func applyHarnessSocketCapability(
	cpReq *hitl.CheckpointRequest,
	grant confine.SocketGrant,
	band wire.ConsequenceBand,
	code wire.ConsequenceCode,
) {
	if grant.ApprovedPath == "" || cpReq == nil {
		return
	}
	approved, resolved := grant.ApprovedPath, grant.ResolvedPath
	if resolved == "" {
		resolved = approved
		grant.ResolvedPath = resolved
	}
	if cpReq.ProposedAction != nil {
		cpReq.ProposedAction.Sockets.SocketGrants = []confine.SocketGrant{grant}
	}
	if band == "" {
		cpReq.ConsequenceBand = wire.ConsequenceBandHighRisk
	}
	if code == "" {
		cpReq.ConsequenceCode = wire.ConsequenceCodeLocalSocket
	}
	cpReq.Explanation = &hitl.ApprovalExplanation{
		What:      fmt.Sprintf(hitl.SocketWhatOne, approved),
		Who:       hitl.WhoAgentCommand,
		IfWrong:   hitl.SocketIfWrong,
		AllowLine: "connections to " + approved,
	}
	cpReq.GrantOffers = capabilitygrants.SocketExecutionGrantOffers(
		hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{
				Tool: "command",
			},
			Scope: hitl.ActionScope{
				SessionID: cpReq.SessionID,
				ProjectID: cpReq.ProjectID,
			},
		},
		[]confine.SocketGrant{grant},
	)
	cpReq.SocketCapability = &hitl.SocketCapability{
		Targets:            []hitl.SocketCapabilityTarget{{ApprovedPath: approved, ResolvedPath: resolved, Binding: grant.Binding}},
		EffectiveAuthority: hitl.SocketAuthorityOutsideSandboxDaemon,
	}
}
