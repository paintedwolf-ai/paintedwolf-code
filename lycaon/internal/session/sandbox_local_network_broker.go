package session

import (
	"context"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// LocalNetworkCheckpointBroker reviews combined listen and connect authority.
type LocalNetworkCheckpointBroker struct {
	Checkpoints       hitl.CheckpointManager
	Store             Store
	Listen            *approvalstate.SandboxPortGrantRuntime
	Loopback          *approvalstate.SandboxPortGrantRuntime
	Provenance        LoopbackProvenanceResolver
	Authority         hitl.ApprovalGate
	ApprovalsDisabled func(projectDir string) bool
	Posture           func(projectDir string) gate.Posture
	Authz             authzledger.Recorder
}

func (b *LocalNetworkCheckpointBroker) posture(projectDir string) gate.Posture {
	if b.Posture == nil {
		return gate.DefaultPosture
	}
	return b.Posture(projectDir)
}

// AwaitCombined implements tools.LocalNetworkGate.
func (b *LocalNetworkCheckpointBroker) AwaitCombined(ctx context.Context, in tools.LocalNetworkAsk) (tools.LocalNetworkResult, error) {
	var out tools.LocalNetworkResult
	if b == nil || b.Listen == nil || b.Loopback == nil {
		return out, nil
	}
	invokingSessionID, rootSessionID := askSessionIDs(ctx, b.Store, in.SessionID, in.ParentSessionID)
	listenPorts := append([]uint16(nil), in.ListenPorts...)
	connectPorts := append([]uint16(nil), in.ConnectPorts...)
	listenGranted, listenCovered := b.Listen.SessionPorts(rootSessionID)
	loopbackGranted, loopbackCovered := b.Loopback.SessionPorts(rootSessionID)
	if listenGranted && loopbackGranted && portLeaseCovers(listenCovered, listenPorts) && portLeaseCovers(loopbackCovered, connectPorts) {
		return tools.LocalNetworkResult{Authorized: true}, nil
	}
	if b.ApprovalsDisabled != nil && b.ApprovalsDisabled(in.ProjectDir) {
		b.Listen.ClearDenied(invokingSessionID, listenAxisKey)
		b.Loopback.ClearDenied(invokingSessionID, loopbackAxisKey)
		b.Listen.GrantSessionPorts(rootSessionID, nil)
		b.Loopback.GrantSessionPorts(rootSessionID, nil)
		return tools.LocalNetworkResult{Authorized: true}, nil
	}

	reach := connectCoGrant(ctx, b.Provenance, invokingSessionID, in.ProjectDir, listenPorts)
	selfSpawned := len(connectPorts) > 0
	for _, cp := range connectPorts {
		if slices.Contains(reach, cp) || sessionOwnsPorts(ctx, b.Provenance, invokingSessionID, in.ProjectDir, []uint16{cp}) {
			continue
		}
		selfSpawned = false
		break
	}

	facts := gate.Facts{
		Stage: gate.StagePreSpawn,
		Ran:   gate.ProducerApprovalRequest,
		CapabilityWidening: &gate.CapabilityWidening{
			Axes:        []string{gate.AxisLocalListen, gate.AxisLoopbackConnect},
			Ports:       append(append([]uint16(nil), listenPorts...), connectPorts...),
			SelfSpawned: selfSpawned,
		},
	}
	verdict, _ := gate.Evaluate(facts, b.posture(in.ProjectDir))
	if verdict != gate.Ask {
		b.Listen.ClearDenied(invokingSessionID, listenAxisKey)
		b.Loopback.ClearDenied(invokingSessionID, loopbackAxisKey)
		b.Listen.GrantSessionPorts(rootSessionID, listenPorts)
		b.Loopback.GrantSessionPorts(rootSessionID, connectPorts)
		if len(reach) > 0 {
			b.Loopback.GrantSessionPorts(rootSessionID, reach)
		}
		return tools.LocalNetworkResult{Authorized: true}, nil
	}

	if b.Checkpoints == nil {
		return out, nil
	}
	return b.raise(ctx, in, invokingSessionID, rootSessionID)
}

func (b *LocalNetworkCheckpointBroker) raise(ctx context.Context, in tools.LocalNetworkAsk, invokingSessionID, rootSessionID string) (tools.LocalNetworkResult, error) {
	resolved, err := awaitSandboxAsk(ctx, sandboxAskRequest{
		Gate: b.Listen, Checkpoints: b.Checkpoints, Authz: b.Authz,
		InvokingSessionID: invokingSessionID, Key: in.SecretPermission.Key(localNetworkAxisKey), ToolCallID: in.ToolCallID,
		ProjectID: in.ProjectID, Tool: "local_network",
		AskFamily: authzledger.AskFamilyLocalListen, CheckpointLabel: "local-network",
		BuildCard: func() (sandboxAskCard, error) { return b.buildCard(in, invokingSessionID, rootSessionID) },
	})
	if err != nil || !resolved.Answered {
		return tools.LocalNetworkResult{}, err
	}
	if resolved.Authorized && b.Loopback != nil {
		b.Loopback.ClearDenied(invokingSessionID, loopbackAxisKey)
	}
	return tools.LocalNetworkResult{
		Raised: resolved.Raised, Authorized: resolved.Authorized, Denied: resolved.Denied,
		SecretApproved: resolved.Authorized && in.SecretPermission != nil, SecretAttestationID: resolved.AttestationID,
		UserGuidance: resolved.UserGuidance,
	}, nil
}

func (b *LocalNetworkCheckpointBroker) buildCard(in tools.LocalNetworkAsk, invokingSessionID, rootSessionID string) (sandboxAskCard, error) {
	summary := sandboxAskCommandSummary(in.Command)
	listenPorts := append([]uint16(nil), in.ListenPorts...)
	connectPorts := append([]uint16(nil), in.ConnectPorts...)
	action := hitl.ProposedAction{
		Tool: "local_network", Args: map[string]any{
			"listen_ports":  listenPortInts(listenPorts),
			"connect_ports": listenPortInts(connectPorts),
		},
		ProjectID: in.ProjectID, ProjectDir: in.ProjectDir, SessionID: invokingSessionID, RootSessionID: rootSessionID,
		Contained: hitl.ContainedForAction(hitl.ActionConfineInputs{
			ProjectID: in.ProjectID, Roots: projectRoots(in.ProjectDir),
			LocalListen: true, LocalListenPorts: listenPorts,
			LoopbackConnect: true, LoopbackConnectPorts: connectPorts,
		}),
	}
	listenGrant := listenChatGrant(action)
	connectGrant := loopbackChatGrant(action)
	listenOptions := portAuthorityLadder(
		"approve_local_network_once",
		"only this invocation",
		"a later action needs local-network authority",
		hitl.AuthorityLocalListenChat, listenGrant, rootSessionID, true,
	)
	connectChat := portAuthorityLadder(
		"approve_local_network_once",
		"only this invocation",
		"a later action needs local-network authority",
		hitl.AuthorityLoopbackConnectChat, connectGrant, rootSessionID, false,
	)
	options := composeLocalNetworkOptions(listenOptions, connectChat)
	decision := capabilityWideningDecisionWithPosture(b.posture(in.ProjectDir), gate.AxisLocalListen, gate.AxisLoopbackConnect)
	options = append(options, hitl.QuietOptions(action, decision, nil, func(key string) bool {
		if b.Authority == nil {
			return false
		}
		_, live := b.Authority.AskQuietLive(action.ChatSession(), key)
		return live
	})...)
	title := "Allow local network use"
	targets := []hitl.ApprovalTarget{
		{Kind: "local_listen", Label: listenGrantLabel(listenPorts), Details: map[string]any{
			"listen_ports": listenPortInts(listenPorts),
		}},
		{Kind: "loopback_connect", Label: loopbackGrantLabel(connectPorts), Details: map[string]any{
			"connect_ports": listenPortInts(connectPorts),
		}},
	}
	primary, cited, reasons := hitl.PresentDecision(decision)
	plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectActionSet, Title: title, Targets: targets,
	}, hitl.ApprovalPresentation{
		Action: "Local server and local connections", Tool: strings.TrimSpace(in.ToolName), Command: summary,
		Impact:    hitl.LocalListenWhat + " " + hitl.LoopbackConnectWhat,
		Who:       hitl.WhoAgentCommand,
		IfWrong:   hitl.LocalListenIfWrong + " " + hitl.LoopbackConnectIfWrong,
		AllowLine: hitl.LocalListenAllowLine + "; " + hitl.LoopbackConnectAllowLine,
		Gate:      primary, Cited: cited,
		ConsequenceBand: string(api.ConsequenceBandStandard),
		ConsequenceCode: string(api.ConsequenceCodeLocalListen),
	}, reasons, options, hitl.FaceContext{})
	if err != nil {
		return sandboxAskCard{}, tools.ApprovalPlanInvalid()
	}
	return composeSandboxSecretCard(sandboxAskCard{Action: action, Title: title, Plan: plan, Decision: decision}, in.SecretPermission)
}

// composeLocalNetworkOptions merges the listen ladder with the matching
// loopback-connect authority so each rung continues both axes.
func composeLocalNetworkOptions(listen, connect []hitl.ApprovalOption) []hitl.ApprovalOption {
	byRung := map[hitl.ApprovalOptionRung]hitl.ApprovalOption{}
	for _, option := range connect {
		byRung[option.Rung] = option
	}
	out := make([]hitl.ApprovalOption, 0, len(listen))
	for _, option := range listen {
		if extra, ok := byRung[option.Rung]; ok {
			option.Authority = append(append([]hitl.ApprovalAuthorityDelta(nil), option.Authority...), extra.Authority...)
		}
		out = append(out, option)
	}
	return out
}
