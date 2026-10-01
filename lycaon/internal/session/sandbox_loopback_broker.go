package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// Loopback grants cover local services without widening external egress.
type LoopbackCheckpointBroker struct {
	Checkpoints       hitl.CheckpointManager
	Store             Store
	Runtime           *approvalstate.SandboxPortGrantRuntime
	Provenance        LoopbackProvenanceResolver
	Authority         hitl.ApprovalGate
	ApprovalsDisabled func(projectDir string) bool
	Posture           func(projectDir string) gate.Posture
	Authz             authzledger.Recorder
}

func (b *LoopbackCheckpointBroker) posture(projectDir string) gate.Posture {
	if b.Posture == nil {
		return gate.DefaultPosture
	}
	return b.Posture(projectDir)
}

// Await implements tools.LoopbackConnectGate.
func (b *LoopbackCheckpointBroker) Await(ctx context.Context, in tools.LoopbackConnectAsk) (tools.LoopbackConnectResult, error) {
	var out tools.LoopbackConnectResult
	if b == nil || b.Runtime == nil {
		return out, nil
	}
	ports := append([]uint16(nil), in.Ports...)
	invokingSessionID, rootSessionID := askSessionIDs(ctx, b.Store, in.SessionID, in.ParentSessionID)
	if granted, covered := b.Runtime.SessionPorts(rootSessionID); granted && portLeaseCovers(covered, ports) {
		return tools.LoopbackConnectResult{Authorized: true, Ports: covered}, nil
	}
	if b.ApprovalsDisabled != nil && b.ApprovalsDisabled(in.ProjectDir) {
		b.Runtime.ClearDenied(invokingSessionID, loopbackAxisKey)
		b.Runtime.GrantSessionPorts(rootSessionID, nil)
		return tools.LoopbackConnectResult{Authorized: true}, nil
	}

	facts := gate.Facts{
		Stage: gate.StagePreSpawn,
		Ran:   gate.ProducerApprovalRequest,
		CapabilityWidening: &gate.CapabilityWidening{
			Axes:        []string{gate.AxisLoopbackConnect},
			Ports:       ports,
			SelfSpawned: sessionOwnsPorts(ctx, b.Provenance, invokingSessionID, in.ProjectDir, ports),
		},
	}
	// The lease is recorded only after the verdict, so an ask a posture keeps
	// cannot be skipped by a retry.
	verdict, _ := gate.Evaluate(facts, b.posture(in.ProjectDir))
	if verdict != gate.Ask {
		b.Runtime.ClearDenied(invokingSessionID, loopbackAxisKey)
		b.Runtime.GrantSessionPorts(rootSessionID, ports)
		return tools.LoopbackConnectResult{Authorized: true, Ports: ports}, nil
	}

	if b.Checkpoints == nil {
		return out, nil
	}
	return b.raise(ctx, in, invokingSessionID, rootSessionID, ports)
}

func (b *LoopbackCheckpointBroker) raise(ctx context.Context, in tools.LoopbackConnectAsk, invokingSessionID, rootSessionID string, ports []uint16) (tools.LoopbackConnectResult, error) {
	resolved, err := awaitSandboxAsk(ctx, sandboxAskRequest{
		Gate: b.Runtime, Checkpoints: b.Checkpoints, Authz: b.Authz,
		InvokingSessionID: invokingSessionID, Key: in.SecretPermission.Key(loopbackAxisKey), ToolCallID: in.ToolCallID,
		ProjectID: in.ProjectID, Tool: "loopback_connect",
		AskFamily: authzledger.AskFamilyLoopbackConnect, CheckpointLabel: "loopback-connect",
		BuildCard: func() (sandboxAskCard, error) { return b.buildCard(in, invokingSessionID, rootSessionID, ports) },
	})
	if err != nil || !resolved.Answered {
		return tools.LoopbackConnectResult{}, err
	}
	return tools.LoopbackConnectResult{
		Raised: resolved.Raised, Authorized: resolved.Authorized, Denied: resolved.Denied,
		SecretApproved: resolved.Authorized && in.SecretPermission != nil,
		Ports:          ports, UserGuidance: resolved.UserGuidance,
	}, nil
}

func (b *LoopbackCheckpointBroker) buildCard(in tools.LoopbackConnectAsk, invokingSessionID, rootSessionID string, ports []uint16) (sandboxAskCard, error) {
	summary := sandboxAskCommandSummary(in.Command)
	label := loopbackGrantLabel(ports)
	action := hitl.ProposedAction{
		Tool: "loopback_connect", Args: map[string]any{"connect_ports": listenPortInts(ports)},
		ProjectID: in.ProjectID, ProjectDir: in.ProjectDir, SessionID: invokingSessionID, RootSessionID: rootSessionID,
		Contained: hitl.ContainedForAction(hitl.ActionConfineInputs{
			ProjectID: in.ProjectID, Roots: projectRoots(in.ProjectDir),
			LoopbackConnect: true, LoopbackConnectPorts: ports,
		}),
	}
	grant := loopbackChatGrant(action)
	options := portAuthorityLadder(
		"approve_loopback_connect_once",
		"only this invocation",
		"a later action needs local client authority",
		hitl.AuthorityLoopbackConnectChat, grant, rootSessionID, false,
	)
	decision := capabilityWideningDecisionWithPosture(b.posture(in.ProjectDir), gate.AxisLoopbackConnect)
	options = append(options, hitl.QuietOptions(action, decision, nil, func(key string) bool {
		if b.Authority == nil {
			return false
		}
		_, live := b.Authority.AskQuietLive(action.ChatSession(), key)
		return live
	})...)
	title := "Allow a local service connection"
	target := hitl.ApprovalTarget{Kind: "loopback_connect", Label: label,
		Details: map[string]any{"connect_ports": listenPortInts(ports)}}
	primary, cited, reasons := hitl.PresentDecision(decision)
	plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectLoopbackConnect, Title: title, Targets: []hitl.ApprovalTarget{target},
	}, hitl.ApprovalPresentation{
		Action: "Local service connection", Tool: strings.TrimSpace(in.ToolName), Command: summary,
		Impact: hitl.LoopbackConnectWhat, Who: hitl.WhoAgentAction,
		IfWrong: hitl.LoopbackConnectIfWrong, AllowLine: hitl.LoopbackConnectAllowLine,
		Gate: primary, Cited: cited, ConsequenceBand: string(api.ConsequenceBandStandard),
		ConsequenceCode: string(api.ConsequenceCodeLoopbackConnect),
	}, reasons, options, hitl.FaceContext{})
	if err != nil {
		return sandboxAskCard{}, tools.ApprovalPlanInvalid()
	}
	return composeSandboxSecretCard(sandboxAskCard{Action: action, Title: title, Plan: plan, Decision: decision}, in.SecretPermission)
}

func loopbackGrantLabel(ports []uint16) string {
	if len(ports) == 0 {
		return "any service on this machine"
	}
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, strconv.Itoa(int(port)))
	}
	return "local destination port " + strings.Join(parts, ", ")
}

func loopbackChatGrant(action hitl.ProposedAction) hitl.ApprovalGrant {
	key := loopbackAxisKey
	raw := strings.Join([]string{string(hitl.ApprovalGrantScopeChat), hitl.ApprovalGrantCategoryLoopbackConnect,
		key, action.ChatSession()}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return hitl.ApprovalGrant{
		ID: "grant_" + hex.EncodeToString(sum[:8]), Scope: hitl.ApprovalGrantScopeChat,
		Predicate:     hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryLoopbackConnect, Pattern: key},
		ChatSessionID: action.ChatSession(), ProjectID: action.ProjectID, ProjectDir: action.ProjectDir,
		Title: hitl.TitleAllowForThisChat, Coverage: "connections to local services in this chat",
		GrantedAt: time.Now().UTC(), ExpiresWhen: hitl.ExpiresWhenChatDeleted,
		ReaskWhen: "this chat is deleted", Source: "checkpoint",
	}
}

// SessionLoopbackGrant implements tools.LoopbackConnectGate.
func (b *LoopbackCheckpointBroker) SessionLoopbackGrant(ctx context.Context, sessionID, parentSessionID string) (bool, []uint16) {
	if b == nil || b.Runtime == nil {
		return false, nil
	}
	_, rootSessionID := askSessionIDs(ctx, b.Store, sessionID, parentSessionID)
	return b.Runtime.SessionPorts(rootSessionID)
}
