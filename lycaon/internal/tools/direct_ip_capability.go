package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// DirectIPLifecyclePhase is a typed lifecycle moment for one-action direct IP.
type DirectIPLifecyclePhase string

const (
	DirectIPLifecycleRequested     DirectIPLifecyclePhase = "requested"
	DirectIPLifecycleApproved      DirectIPLifecyclePhase = "approved"
	DirectIPLifecycleLeaseReused   DirectIPLifecyclePhase = "lease_reused"
	DirectIPLifecycleDenied        DirectIPLifecyclePhase = "denied"
	DirectIPLifecycleStarted       DirectIPLifecyclePhase = "started"
	DirectIPLifecycleCompleted     DirectIPLifecyclePhase = "completed"
	DirectIPLifecycleReconstructed DirectIPLifecyclePhase = "reconstructed"
)

// DirectIPLifecycleEvent is consumed by capability audit surfaces.
type DirectIPLifecycleEvent struct {
	Phase                DirectIPLifecyclePhase
	SessionID            string
	ToolCallID           string
	ActionDigest         string
	AuthorizationSource  string
	Visibility           string // fixed unobserved
	DeclaredDestinations []string
	Background           bool
}

// DirectIPLifecycleHook receives typed unobserved direct lifecycle facts.
type DirectIPLifecycleHook func(DirectIPLifecycleEvent)

// directIPCapabilityPreflightResult is the executor-facing outcome of direct-IP preflight.
type directIPCapabilityPreflightResult struct {
	Requested         bool
	Declared          []string
	ActionDigest      string
	RequestDigest     string
	ConfineDigest     string
	Authorized        bool
	ApprovalSatisfied bool
}

// directIPApprovalReview is the shared direct-network approval input.
type directIPApprovalReview struct {
	Action hitl.ProposedAction
	Lease  hitl.DirectIPLease
}

func (e *DefaultToolExecutor) buildDirectIPApprovalReview(ctx context.Context, tool string, args map[string]any, tc ToolContext, declared []string) directIPApprovalReview {
	action, lease := DirectIPReview(tool, args, tc, declared, e.overlayWriteRoots(ctx, tc))
	return directIPApprovalReview{Action: action, Lease: lease}
}

// DirectIPReview is the exact-action identity a direct-IP card and lease share.
func DirectIPReview(tool string, args map[string]any, tc ToolContext, declared, overlayWriteRoots []string) (hitl.ProposedAction, hitl.DirectIPLease) {
	rootSession := tc.ChatSessionID()
	confInputs := ActionConfineInputsForContext(tc, overlayWriteRoots)
	confInputs.DirectIP = true
	confInputs.DirectIPDeclared = declared
	confInputs.SocksProxyEnv = false
	contained := hitl.ContainedForAction(confInputs)
	action := hitl.ProposedAction{
		Tool:                    tool,
		Args:                    args,
		Files:                   filesFromArgs(tool, args),
		ResolvedFiles:           resolvedApprovalFiles(tool, args, tc),
		Command:                 commandsurface.PrimaryCommandLine(args, nil),
		ProjectID:               tc.ProjectID,
		ProjectDir:              tc.ActiveRootPath(),
		SessionID:               tc.SessionID,
		RootSessionID:           rootSession,
		SessionScratchRoot:      tc.SessionScratchDir,
		SocketGrants:            append([]confine.SocketGrant(nil), tc.SocketGrants...),
		SocketScopes:            append([]string(nil), tc.SocketScopes...),
		SocketGrantStates:       append([]string(nil), tc.SocketGrantStates...),
		AuthorizedSocketDigests: append([]string(nil), tc.AuthorizedSocketDigests...),
		Contained:               contained,
		ActionID:                tc.ToolCallID,
		DirectIPRequested:       true,
		Visibility:              hitl.DirectIPVisibilityUnobserved,
		DeclaredDestinations:    append([]string(nil), declared...),
		HostResources:           append([]string(nil), tc.HostResources...),
		HostResourceFamilies:    append([]string(nil), tc.HostResourceFamilies...),
	}
	lease := hitl.DirectIPLease{
		ActionDigest:          hitl.GrantKey(action),
		RequestDigest:         directIPRequestDigest(declared),
		ConfinementDigest:     directIPConfinementDigest(contained),
		ChatConfinementDigest: hitl.DirectIPChatConfinementDigest(contained.Roots, contained.Egress),
		DeclaredDestinations:  append([]string(nil), declared...),
		CommandSummary:        action.Command,
	}
	return action, lease
}

// SetDirectIPCapabilityRuntime wires one-action direct-IP permits.
func (e *DefaultToolExecutor) SetDirectIPCapabilityRuntime(rt DirectIPCapabilityRuntime) {
	if e != nil {
		e.directIPRuntime = rt
	}
}

// SetDirectIPLifecycleHook installs the optional lifecycle callback for capability audit surfaces.
func (e *DefaultToolExecutor) SetDirectIPLifecycleHook(hook DirectIPLifecycleHook) {
	if e != nil {
		e.directIPLifecycle = hook
	}
}

func (e *DefaultToolExecutor) emitDirectIPLifecycle(ev DirectIPLifecycleEvent) {
	if e == nil || e.directIPLifecycle == nil {
		return
	}
	ev.Visibility = hitl.DirectIPVisibilityUnobserved
	e.directIPLifecycle(ev)
}

func (e *DefaultToolExecutor) preflightDirectIPCapability(
	ctx context.Context,
	tool string,
	args map[string]any,
	tc ToolContext,
	approvalAlreadySatisfied bool,
) (*directIPCapabilityPreflightResult, error) {
	if !tc.Invocation.Contract.Supports(toolcontract.CapabilityDirectIP) {
		return nil, nil
	}
	capReq, reject := ParseCapabilityRequest(args)
	if reject != nil {
		return nil, e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, reject)
	}
	if capReq == nil || capReq.DirectIP == nil {
		return nil, nil
	}
	if tc.SocksProxyEnv {
		return nil, e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &ToolReject{
			Code: isolation.CodeSocksProxyInvalid, Data: map[string]any{
				"reason": "socks_proxy is incompatible with capability_request.direct_ip",
			}})
	}
	declared := append([]string(nil), capReq.DirectIP.DeclaredDestinations...)
	review := e.buildDirectIPApprovalReview(ctx, tool, args, tc, declared)
	action, lease := review.Action, review.Lease
	if !lease.Complete() {
		return nil, e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, ApprovalPlanInvalid())
	}
	actionDigest := lease.ActionDigest
	requestDigest := lease.RequestDigest
	confineDigest := lease.ConfinementDigest
	e.emitDirectIPLifecycle(DirectIPLifecycleEvent{
		Phase:                DirectIPLifecycleRequested,
		SessionID:            tc.SessionID,
		ToolCallID:           tc.ToolCallID,
		ActionDigest:         actionDigest,
		DeclaredDestinations: declared,
		Background:           boolArg(args, "background"),
	})
	if e.directIPRuntime != nil && e.directIPRuntime.Authorized(tc.SessionID, tc.ToolCallID, actionDigest) {
		e.emitDirectIPLifecycle(DirectIPLifecycleEvent{
			Phase:                DirectIPLifecycleApproved,
			SessionID:            tc.SessionID,
			ToolCallID:           tc.ToolCallID,
			ActionDigest:         actionDigest,
			AuthorizationSource:  authzledger.AuthorizationSourceHuman,
			DeclaredDestinations: declared,
			Background:           boolArg(args, "background"),
		})
		return &directIPCapabilityPreflightResult{
			Requested:         true,
			Declared:          declared,
			ActionDigest:      actionDigest,
			RequestDigest:     requestDigest,
			ConfineDigest:     confineDigest,
			Authorized:        true,
			ApprovalSatisfied: true,
		}, nil
	}
	if e.approvalsDisabled != nil && e.approvalsDisabled(tc.ActiveRootPath()) {
		if e.directIPRuntime != nil {
			e.directIPRuntime.IssuePermit(tc.SessionID, tc.ToolCallID, actionDigest, requestDigest, confineDigest)
		}
		e.emitDirectIPLifecycle(DirectIPLifecycleEvent{
			Phase:                DirectIPLifecycleApproved,
			SessionID:            tc.SessionID,
			ToolCallID:           tc.ToolCallID,
			ActionDigest:         actionDigest,
			AuthorizationSource:  authzledger.AuthorizationSourceNeverAsk,
			DeclaredDestinations: declared,
			Background:           boolArg(args, "background"),
		})
		return &directIPCapabilityPreflightResult{
			Requested:         true,
			Declared:          declared,
			ActionDigest:      actionDigest,
			RequestDigest:     requestDigest,
			ConfineDigest:     confineDigest,
			Authorized:        true,
			ApprovalSatisfied: true,
		}, nil
	}
	var approval *hitl.ApprovalResult
	if !approvalAlreadySatisfied {
		pre, err := e.evaluatePreSpawn(ctx, action)
		if err != nil {
			return nil, err
		}
		if pre != nil && pre.Denied {
			return nil, e.rejectBoundaryPolicyDeny(ctx, tool, args, tc, pre)
		}
		approval = pre
		// A chat lease answers direct networking only. Settling here skips the
		// policy stage, so any other reason on the action must reach its card.
		if directIPLeaseSettles(approval) && e.directIPLeaseAuthorizes(action, tc, lease) {
			return &directIPCapabilityPreflightResult{
				Requested:         true,
				Declared:          declared,
				ActionDigest:      actionDigest,
				RequestDigest:     requestDigest,
				ConfineDigest:     confineDigest,
				Authorized:        true,
				ApprovalSatisfied: true,
			}, nil
		}
	}
	if err := e.awaitDirectIPCapability(ctx, action, lease, tc, approval); err != nil {
		return nil, e.rejectApprovalErr(ctx, tool, tc.Agent, args, err)
	}
	authorized := e.directIPRuntime != nil && e.directIPRuntime.Authorized(tc.SessionID, tc.ToolCallID, actionDigest)
	if !authorized {
		return nil, e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &ToolReject{
			Code: isolation.CodeDirectIPAuthorizationChanged,
			Data: map[string]any{"reason": "approval did not install current-call direct network authority"},
		})
	}
	return &directIPCapabilityPreflightResult{
		Requested:         true,
		Declared:          declared,
		ActionDigest:      actionDigest,
		RequestDigest:     requestDigest,
		ConfineDigest:     confineDigest,
		Authorized:        true,
		ApprovalSatisfied: true,
	}, nil
}

// directIPLeaseSettles reports whether a chat lease may answer this gate result.
func directIPLeaseSettles(approval *hitl.ApprovalResult) bool {
	if approval == nil || approval.Decision == nil {
		return true
	}
	return approval.Decision.OnlyDirectIPChannel()
}

// directIPLeaseAuthorizes issues a permit from a matching chat lease.
func (e *DefaultToolExecutor) directIPLeaseAuthorizes(action hitl.ProposedAction, tc ToolContext, lease hitl.DirectIPLease) bool {
	if e == nil || e.directIPRuntime == nil || !lease.Complete() {
		return false
	}
	if !e.directIPRuntime.LeaseCovers(action.ChatSession(), lease) {
		return false
	}
	e.directIPRuntime.IssuePermit(tc.SessionID, tc.ToolCallID, lease.ActionDigest, lease.RequestDigest, lease.ConfinementDigest)
	e.emitDirectIPLifecycle(DirectIPLifecycleEvent{
		Phase:                DirectIPLifecycleLeaseReused,
		SessionID:            action.SessionID,
		ToolCallID:           tc.ToolCallID,
		ActionDigest:         lease.ActionDigest,
		AuthorizationSource:  authzledger.AuthorizationSourceLease,
		DeclaredDestinations: lease.DeclaredDestinations,
		Background:           boolArg(action.Args, "background"),
	})
	return true
}

func (e *DefaultToolExecutor) awaitDirectIPCapability(
	ctx context.Context,
	action hitl.ProposedAction,
	lease hitl.DirectIPLease,
	tc ToolContext,
	approval *hitl.ApprovalResult,
) error {
	actionDigest, requestDigest := lease.ActionDigest, lease.RequestDigest
	declared := lease.DeclaredDestinations
	explanation := &hitl.ApprovalExplanation{
		What:      hitl.DirectIPWhat,
		Who:       hitl.WhoAgentCommand,
		IfWrong:   hitl.DirectIPIfWrong,
		AllowLine: hitl.DirectIPAllowLine,
	}
	payload := &hitl.DirectIPCapability{
		Visibility:           hitl.DirectIPVisibilityUnobserved,
		AFUnix:               hitl.DirectIPAFUnixExactGrantsOnly,
		DeclaredDestinations: append([]string(nil), declared...),
		ActionDigest:         actionDigest,
		CommandSummary:       action.Command,
	}
	// Withheld reuse offers no grant rungs.
	var ladder, grantOffers []hitl.ApprovalGrantOffer
	title := "Run once with direct network access"
	if approval == nil || approval.Decision == nil || approval.Decision.Reuse().Offered() {
		ladder = DirectIPExecutionGrantOffers(action, lease)
		grantOffers = append(append(grantOffers, ladder...), e.absorbedGrantOffers(action, approval)...)
		title = "Run with direct network access"
	}
	permit := hitl.ApprovalAuthorityDelta{
		Kind: hitl.AuthorityDirectIPPermit, SessionID: action.SessionID, ToolCallID: tc.ToolCallID,
		ActionDigest: actionDigest, DirectIPLease: &lease,
	}
	options := []hitl.ApprovalOption{{
		ID: "approve_direct_ip_once", Kind: hitl.ApprovalOptionCurrentAction, Rung: hitl.ApprovalRungOnce,
		Title: hitl.TitleAllowOnce, Coverage: "only this exact direct-network action",
		ExpiresWhen: hitl.ExpiresAfterThisAction, ReaskWhen: hitl.ReaskWhenActionRunsAgain,
		DecisionAction: "approve",
		Authority:      []hitl.ApprovalAuthorityDelta{permit},
	}}
	for _, offer := range ladder {
		options = append(options, hitl.ContinuingLeaseOption(offer, permit))
	}
	for _, offer := range grantOffers[len(ladder):] {
		options = append(options, hitl.ContinuingLeaseOption(offer, permit))
	}
	primaryGate, cited, reasons := hitl.PresentDecision(approvalDecision(approval))
	options = append(options, e.quietOptionsFor(action, approvalDecision(approval))...)
	plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectDirectIP, Title: title,
		Targets: []hitl.ApprovalTarget{{Kind: "direct_ip", Label: DirectIPTargetLabel(action.Command), Details: map[string]any{"declared_destinations": declared, "visibility": "unobserved"}}},
	}, hitl.ApprovalPresentation{
		Action: "Use direct network access", Tool: action.Tool, Command: action.Command,
		Impact: explanation.What, Who: explanation.Who, IfWrong: explanation.IfWrong, AllowLine: explanation.AllowLine,
		Gate: primaryGate, Cited: cited, GrantDelta: approvalGrantDelta(approval),
		Detection: detectionOf(approval),
	}, reasons, options, hitl.FaceContext{})
	if err != nil {
		return ApprovalPlanInvalid()
	}
	permission, err := e.prepareSecretPermission(ctx, action.Tool, action.Args, tc)
	if err != nil {
		return err
	}
	plan, err = hitl.ComposeSecretPermission(plan, action, permission, hitl.FaceContext{})
	if err != nil {
		return ApprovalPlanInvalid()
	}
	final, err := e.raiseAndWaitToolApproval(ctx, toolApprovalRaise{
		Action:                 action,
		Plan:                   plan,
		SecretScreenHit:        permission != nil,
		Title:                  title,
		ToolCallID:             tc.ToolCallID,
		ProjectID:              tc.ProjectID,
		Explanation:            explanation,
		CoalesceKey:            permission.Key(directIPCoalesceKey(actionDigest, requestDigest)),
		DirectIPCapability:     payload,
		SkipGrantOfferAutofill: true,
		ApprovalMatches:        approvalRuleMatches(approval),
		Decision:               approvalDecision(approval),
		Detection:              detectionOf(approval),
		GrantDelta:             approvalGrantDelta(approval),
		GrantOffers:            grantOffers,
	})
	if err != nil {
		return err
	}
	switch {
	case hitl.CheckpointAuthorizes(final):
		approveCapabilitySecretPermission(ctx, tc, permission)
		e.emitDirectIPLifecycle(DirectIPLifecycleEvent{
			Phase:                DirectIPLifecycleApproved,
			SessionID:            action.SessionID,
			ToolCallID:           tc.ToolCallID,
			ActionDigest:         actionDigest,
			AuthorizationSource:  authzledger.AuthorizationSourceHuman,
			DeclaredDestinations: declared,
		})
		return nil
	case final.Status == hitl.DecisionStatusRejected || final.Status == hitl.DecisionStatusApproved:
		e.emitDirectIPLifecycle(DirectIPLifecycleEvent{
			Phase:                DirectIPLifecycleDenied,
			SessionID:            action.SessionID,
			ToolCallID:           tc.ToolCallID,
			ActionDigest:         actionDigest,
			AuthorizationSource:  authzledger.AuthorizationSourceHuman,
			DeclaredDestinations: declared,
		})
		return isolationCheckpointReject(isolation.CodeDirectIPDenied, final)
	case final.Status == hitl.DecisionStatusExpired:
		e.emitDirectIPLifecycle(DirectIPLifecycleEvent{
			Phase:                DirectIPLifecycleDenied,
			SessionID:            action.SessionID,
			ToolCallID:           tc.ToolCallID,
			ActionDigest:         actionDigest,
			AuthorizationSource:  authzledger.AuthorizationSourceExpiry,
			DeclaredDestinations: declared,
		})
		return isolationCheckpointReject(isolation.CodeDirectIPDenied, final)
	case final.Status == hitl.DecisionStatusCanceled:
		source := authzledger.AuthorizationSourceUserStop
		if final.ResolvedBy == authzledger.ResolvedByHostStop {
			source = authzledger.AuthorizationSourceHostStop
		}
		e.emitDirectIPLifecycle(DirectIPLifecycleEvent{
			Phase:                DirectIPLifecycleDenied,
			SessionID:            action.SessionID,
			ToolCallID:           tc.ToolCallID,
			ActionDigest:         actionDigest,
			AuthorizationSource:  source,
			DeclaredDestinations: declared,
		})
		return isolationCheckpointReject(isolation.CodeDirectIPDenied, final)
	default:
		return isolationCheckpointReject(isolation.CodeDirectIPDenied, final)
	}
}

func DirectIPTargetLabel(command string) string {
	if command = strings.TrimSpace(command); command != "" {
		return command
	}
	return "Direct outbound networking"
}

// FinalizeDirectIPForSpawn consumes the current-call direct-IP permit immediately before spawn.
func FinalizeDirectIPForSpawn(tctx ToolContext) *ToolReject {
	if !tctx.DirectIPRequested {
		return nil
	}
	if tctx.DirectIPCapabilityRuntime == nil {
		return &ToolReject{Code: isolation.CodeDirectIPAuthorizationChanged, Data: map[string]any{
			"reason": "missing direct IP capability runtime",
		}}
	}
	ok, err := tctx.DirectIPCapabilityRuntime.ConsumePermit(tctx.SessionID, tctx.ToolCallID, tctx.DirectIPActionDigest, tctx.DirectIPRequestDigest, tctx.DirectIPConfineDigest)
	if err != nil {
		return &ToolReject{Code: isolation.CodeDirectIPAuthorizationChanged, Data: map[string]any{
			"reason": err.Error(),
		}}
	}
	if !ok {
		return &ToolReject{Code: isolation.CodeDirectIPAuthorizationChanged, Data: map[string]any{
			"reason": "missing current-call direct IP permit",
		}}
	}
	return nil
}

func directIPRequestDigest(declared []string) string {
	cp := append([]string(nil), declared...)
	sort.Strings(cp)
	sum := sha256.Sum256([]byte(strings.Join(cp, "\x00")))
	return hex.EncodeToString(sum[:])
}

func directIPConfinementDigest(c hitl.Contained) string {
	parts := []string{
		hitl.RootsDigest(c.Roots),
		c.Egress,
		directIPNarrowingDigest(c),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x01")))
	return hex.EncodeToString(sum[:])
}

// directIPNarrowingDigest binds transport and port scope.
func directIPNarrowingDigest(c hitl.Contained) string {
	for _, permit := range c.EffectiveBoundaryPermits() {
		if permit.Kind == hitl.BoundaryPermitDirectIP && strings.TrimSpace(permit.Digest) != "" {
			return permit.Digest
		}
	}
	if c.DirectIP {
		return "enabled"
	}
	return ""
}

func directIPCoalesceKey(actionDigest, requestDigest string) string {
	if strings.TrimSpace(actionDigest) == "" || strings.TrimSpace(requestDigest) == "" {
		return ""
	}
	return actionDigest + "\x00direct_ip\x00" + requestDigest
}

func boolArg(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	v, ok := args[key].(bool)
	return ok && v
}

func directIPVisibility(requested bool) string {
	if requested {
		return hitl.DirectIPVisibilityUnobserved
	}
	return "none"
}
