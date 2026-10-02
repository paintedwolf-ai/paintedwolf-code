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

// Listener grants preserve mediated egress.
type ListenCheckpointBroker struct {
	Checkpoints hitl.CheckpointManager
	Store       Store
	Runtime     *approvalstate.SandboxPortGrantRuntime
	Loopback    *approvalstate.SandboxPortGrantRuntime
	Authority   hitl.ApprovalGate
	Provenance  LoopbackProvenanceResolver
	// ApprovalsDisabled authorizes the request without a prompt.
	ApprovalsDisabled func(projectDir string) bool
	Posture           func(projectDir string) gate.Posture
	Authz             authzledger.Recorder
}

func (b *ListenCheckpointBroker) posture(projectDir string) gate.Posture {
	if b.Posture == nil {
		return gate.DefaultPosture
	}
	return b.Posture(projectDir)
}

// Await implements tools.LocalListenGate.
func (b *ListenCheckpointBroker) Await(ctx context.Context, in tools.LocalListenAsk) (tools.LocalListenResult, error) {
	var out tools.LocalListenResult
	if b == nil || b.Runtime == nil {
		return out, nil
	}
	ports := append([]uint16(nil), in.Ports...)
	invokingSessionID, rootSessionID := askSessionIDs(ctx, b.Store, in.SessionID, in.ParentSessionID)
	if granted, covered := b.Runtime.SessionPorts(rootSessionID); granted && portLeaseCovers(covered, ports) {
		return tools.LocalListenResult{Authorized: true, Ports: covered}, nil
	}
	if b.ApprovalsDisabled != nil && b.ApprovalsDisabled(in.ProjectDir) {
		b.Runtime.ClearDenied(invokingSessionID, listenAxisKey)
		b.Runtime.GrantSessionPorts(rootSessionID, nil)
		if b.Loopback != nil {
			b.Loopback.ClearDenied(invokingSessionID, loopbackAxisKey)
			b.Loopback.GrantSessionPorts(rootSessionID, nil)
		}
		return tools.LocalListenResult{Authorized: true}, nil
	}

	facts := gate.Facts{
		Stage: gate.StagePreSpawn,
		Ran:   gate.ProducerApprovalRequest,
		CapabilityWidening: &gate.CapabilityWidening{
			Axes:  []string{gate.AxisLocalListen},
			Ports: ports,
		},
	}
	verdict, _ := gate.Evaluate(facts, b.posture(in.ProjectDir))
	if verdict != gate.Ask {
		b.Runtime.ClearDenied(invokingSessionID, listenAxisKey)
		b.Runtime.GrantSessionPorts(rootSessionID, ports)
		if reach := connectCoGrant(ctx, b.Provenance, invokingSessionID, in.ProjectDir, ports); len(reach) > 0 && b.Loopback != nil {
			b.Loopback.ClearDenied(invokingSessionID, loopbackAxisKey)
			b.Loopback.GrantSessionPorts(rootSessionID, reach)
		}
		return tools.LocalListenResult{Authorized: true, Ports: ports}, nil
	}

	if b.Checkpoints == nil {
		return out, nil
	}
	return b.raiseListenCard(ctx, in, invokingSessionID, rootSessionID, ports)
}

// connectCoGrant is the connect authority a listener grant carries: the
// declared ports only, minus any a listener this session does not own already
// holds. An undeclared port set carries none.
func connectCoGrant(ctx context.Context, provenance LoopbackProvenanceResolver, invokingSessionID, projectDir string, ports []uint16) []uint16 {
	if provenance == nil {
		return nil
	}
	var reach []uint16
	for _, p := range ports {
		if !provenance.HeldByOther(ctx, invokingSessionID, projectDir, p) {
			reach = append(reach, p)
		}
	}
	return reach
}

// sessionOwnsPorts reports whether every requested port is served by this session.
// An unnarrowed request is never owned.
func sessionOwnsPorts(ctx context.Context, provenance LoopbackProvenanceResolver, invokingSessionID, projectDir string, ports []uint16) bool {
	if provenance == nil || len(ports) == 0 {
		return false
	}
	for _, p := range ports {
		if owned, _ := provenance.IsSessionOwned(ctx, invokingSessionID, projectDir, p); !owned {
			return false
		}
	}
	return true
}

// Empty port sets mean unrestricted ports on both asks and leases.
func portLeaseCovers(covered, ports []uint16) bool {
	if len(covered) == 0 {
		return true
	}
	if len(ports) == 0 {
		return false
	}
	set := make(map[uint16]struct{}, len(covered))
	for _, p := range covered {
		set[p] = struct{}{}
	}
	for _, p := range ports {
		if _, ok := set[p]; !ok {
			return false
		}
	}
	return true
}

func (b *ListenCheckpointBroker) raiseListenCard(
	ctx context.Context,
	in tools.LocalListenAsk,
	invokingSessionID, rootSessionID string,
	ports []uint16,
) (tools.LocalListenResult, error) {
	resolved, err := awaitSandboxAsk(ctx, sandboxAskRequest{
		Gate: b.Runtime, Checkpoints: b.Checkpoints, Authz: b.Authz,
		InvokingSessionID: invokingSessionID, Key: in.SecretPermission.Key(listenAxisKey), ToolCallID: in.ToolCallID,
		ProjectID: in.ProjectID, Tool: "local_listen",
		AskFamily: authzledger.AskFamilyLocalListen, CheckpointLabel: "local-listen",
		BuildCard: func() (sandboxAskCard, error) { return b.buildListenCard(in, invokingSessionID, rootSessionID, ports) },
	})
	if err != nil || !resolved.Answered {
		return tools.LocalListenResult{}, err
	}
	if resolved.Authorized && b.Loopback != nil {
		b.Loopback.ClearDenied(invokingSessionID, loopbackAxisKey)
	}
	return tools.LocalListenResult{
		Raised: resolved.Raised, Authorized: resolved.Authorized, Denied: resolved.Denied,
		SecretApproved: resolved.Authorized && in.SecretPermission != nil, SecretAttestationID: resolved.AttestationID,
		Ports: ports, UserGuidance: resolved.UserGuidance,
	}, nil
}

func (b *ListenCheckpointBroker) buildListenCard(
	in tools.LocalListenAsk,
	invokingSessionID, rootSessionID string,
	ports []uint16,
) (sandboxAskCard, error) {
	summary := sandboxAskCommandSummary(in.Command)
	label := listenGrantLabel(ports)
	grantAction := hitl.ProposedAction{
		Tool: "local_listen", Args: map[string]any{"listen_ports": listenPortInts(ports)},
		ProjectID: in.ProjectID, ProjectDir: in.ProjectDir, SessionID: invokingSessionID, RootSessionID: rootSessionID,
		Contained: hitl.ContainedForAction(hitl.ActionConfineInputs{
			ProjectID:        in.ProjectID,
			Roots:            projectRoots(in.ProjectDir),
			LocalListen:      true,
			LocalListenPorts: ports,
		}),
	}
	chatGrant := listenChatGrant(grantAction)
	options := portAuthorityLadder(
		"approve_local_listen_once",
		"only this invocation",
		"a later action needs listener authority",
		hitl.AuthorityLocalListenChat, chatGrant, rootSessionID, true,
	)
	decision := capabilityWideningDecisionWithPosture(b.posture(in.ProjectDir), gate.AxisLocalListen)
	options = append(options, hitl.QuietOptions(grantAction, decision, nil, func(key string) bool {
		if b.Authority == nil {
			return false
		}
		_, live := b.Authority.AskQuietLive(grantAction.ChatSession(), key)
		return live
	})...)
	title := "Allow a local server"
	target := hitl.ApprovalTarget{Kind: "local_listen", Label: label, Details: map[string]any{
		"listen_ports": listenPortInts(ports),
	}}
	primary, cited, reasons := hitl.PresentDecision(decision)
	plan, err := hitl.NewApprovalPlan(grantAction, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectLocalListen, Title: title, Targets: []hitl.ApprovalTarget{target},
	}, hitl.ApprovalPresentation{
		Action: "Local server", Tool: strings.TrimSpace(in.ToolName), Command: summary,
		Impact:          hitl.LocalListenWhat,
		Who:             hitl.WhoAgentCommand,
		IfWrong:         hitl.LocalListenIfWrong,
		AllowLine:       hitl.LocalListenAllowLine,
		Gate:            primary,
		Cited:           cited,
		ConsequenceBand: string(api.ConsequenceBandStandard),
		ConsequenceCode: string(api.ConsequenceCodeLocalListen),
	}, reasons, options, hitl.FaceContext{})
	if err != nil {
		return sandboxAskCard{}, tools.ApprovalPlanInvalid()
	}
	return composeSandboxSecretCard(sandboxAskCard{Action: grantAction, Title: title, Plan: plan, Decision: decision}, in.SecretPermission)
}

// listenGrantLabel is the card's target label for a port set.
func listenGrantLabel(ports []uint16) string {
	if len(ports) == 0 {
		return "any local port"
	}
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, strconv.Itoa(int(p)))
	}
	return "local port " + strings.Join(parts, ", ")
}

// listenPortInts converts for JSON args and target details.
func listenPortInts(ports []uint16) []int {
	out := make([]int, 0, len(ports))
	for _, p := range ports {
		out = append(out, int(p))
	}
	return out
}

func listenChatGrant(action hitl.ProposedAction) hitl.ApprovalGrant {
	key := listenAxisKey
	raw := strings.Join([]string{
		string(hitl.ApprovalGrantScopeChat), hitl.ApprovalGrantCategoryLocalListen,
		key, action.ChatSession(),
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return hitl.ApprovalGrant{
		ID: "grant_" + hex.EncodeToString(sum[:8]), Scope: hitl.ApprovalGrantScopeChat,
		Predicate:     hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryLocalListen, Pattern: key},
		ChatSessionID: action.ChatSession(), ProjectID: action.ProjectID, ProjectDir: action.ProjectDir,
		Title: hitl.TitleAllowForThisChat, Coverage: "binding local server ports in this chat",
		GrantedAt: time.Now().UTC(), ExpiresWhen: hitl.ExpiresWhenChatDeleted,
		ReaskWhen: "this chat is deleted", Source: "checkpoint",
	}
}

// SessionListenGrant implements tools.LocalListenGate.
func (b *ListenCheckpointBroker) SessionListenGrant(ctx context.Context, sessionID, parentSessionID string) (bool, []uint16) {
	if b == nil || b.Runtime == nil {
		return false, nil
	}
	_, rootSessionID := askSessionIDs(ctx, b.Store, sessionID, parentSessionID)
	return b.Runtime.SessionPorts(rootSessionID)
}
