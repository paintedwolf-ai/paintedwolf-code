package toolexecution

import (
	"github.com/lycaon/lycaon/internal/capabilitygrants"

	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

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

func (e *Capabilities) buildDirectIPApprovalReview(ctx context.Context, tool string, args map[string]any, tc tools.ToolContext, declared []string) directIPApprovalReview {
	action, lease := DirectIPReview(tool, args, tc, declared, e.Boundary.overlayWriteRoots(ctx, tc))
	return directIPApprovalReview{Action: action, Lease: lease}
}

// DirectIPReview is the exact-action identity a direct-IP card and lease share.
func DirectIPReview(tool string, args map[string]any, tc tools.ToolContext, declared, overlayWriteRoots []string) (hitl.ProposedAction, hitl.DirectIPLease) {
	rootSession := tc.ChatSessionID()
	confInputs := tools.ActionConfineInputsForContext(tc, overlayWriteRoots)
	confInputs.DirectIP = true
	confInputs.DirectIPDeclared = declared
	confInputs.SocksProxyEnv = false
	contained := hitl.ContainedForAction(confInputs)
	action := hitl.ProposedAction{
		Tool:                    tool,
		Args:                    args,
		Files:                   filesFromArgs(tool, args),
		ResolvedFiles:           ResolvedApprovalFiles(tool, args, tc),
		Command:                 commandsurface.PrimaryCommandLine(args, nil),
		ProjectID:               tc.Identity.ProjectID,
		ProjectDir:              tc.ActiveRootPath(),
		SessionID:               tc.Identity.SessionID,
		RootSessionID:           rootSession,
		SessionScratchRoot:      tc.Host.SessionScratchDir,
		SocketGrants:            append([]confine.SocketGrant(nil), tc.Socket.SocketGrants...),
		SocketScopes:            append([]string(nil), tc.Socket.SocketScopes...),
		SocketGrantStates:       append([]string(nil), tc.Socket.SocketGrantStates...),
		AuthorizedSocketDigests: append([]string(nil), tc.Socket.AuthorizedSocketDigests...),
		Contained:               contained,
		ActionID:                tc.Identity.ToolCallID,
		DirectIPRequested:       true,
		Visibility:              hitl.DirectIPVisibilityUnobserved,
		DeclaredDestinations:    append([]string(nil), declared...),
		HostResources:           append([]string(nil), tc.Host.HostResources...),
		HostResourceFamilies:    append([]string(nil), tc.Host.HostResourceFamilies...),
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
func (e *Capabilities) SetDirectIPCapabilityRuntime(rt tools.DirectIPCapabilityRuntime) {
	if e != nil {
		e.directIPRuntime = rt
	}
}

// SetDirectIPLifecycleHook installs the optional lifecycle callback for capability audit surfaces.
func (e *Capabilities) SetDirectIPLifecycleHook(hook tools.DirectIPLifecycleHook) {
	if e != nil {
		e.directIPLifecycle = hook
	}
}

func (e *Capabilities) emitDirectIPLifecycle(ev tools.DirectIPLifecycleEvent) {
	if e == nil || e.directIPLifecycle == nil {
		return
	}
	ev.Visibility = hitl.DirectIPVisibilityUnobserved
	e.directIPLifecycle(ev)
}

func (e *Capabilities) preflightDirectIPCapability(
	ctx context.Context,
	tool string,
	args map[string]any,
	tc tools.ToolContext,
	approvalAlreadySatisfied bool,
) (*directIPCapabilityPreflightResult, error) {
	if !tc.Invocation.Contract.Supports(toolcontract.CapabilityDirectIP) {
		return nil, nil
	}
	capReq, reject := capabilityrequest.ParseCapabilityRequest(args)
	if reject != nil {
		return nil, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, reject)
	}
	if capReq == nil || capReq.DirectIP == nil {
		return nil, nil
	}
	if tc.Local.SocksProxyEnv {
		return nil, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, &toolrejection.ToolReject{
			Code: isolation.CodeSocksProxyInvalid, Data: map[string]any{
				"reason": "socks_proxy is incompatible with capability_request.direct_ip",
			}})
	}
	declared := append([]string(nil), capReq.DirectIP.DeclaredDestinations...)
	review := e.buildDirectIPApprovalReview(ctx, tool, args, tc, declared)
	action, lease := review.Action, review.Lease
	if !lease.Complete() {
		return nil, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, toolrejection.ApprovalPlanInvalid())
	}
	actionDigest := lease.ActionDigest
	requestDigest := lease.RequestDigest
	confineDigest := lease.ConfinementDigest
	e.emitDirectIPLifecycle(tools.DirectIPLifecycleEvent{
		Phase:                tools.DirectIPLifecycleRequested,
		SessionID:            tc.Identity.SessionID,
		ToolCallID:           tc.Identity.ToolCallID,
		ActionDigest:         actionDigest,
		DeclaredDestinations: declared,
		Background:           capabilityrequest.BoolArg(args, "background"),
	})
	if e.directIPRuntime != nil && e.directIPRuntime.Authorized(tc.Identity.SessionID, tc.Identity.ToolCallID, actionDigest) {
		e.emitDirectIPLifecycle(tools.DirectIPLifecycleEvent{
			Phase:                tools.DirectIPLifecycleApproved,
			SessionID:            tc.Identity.SessionID,
			ToolCallID:           tc.Identity.ToolCallID,
			ActionDigest:         actionDigest,
			AuthorizationSource:  authzledger.AuthorizationSourceHuman,
			DeclaredDestinations: declared,
			Background:           capabilityrequest.BoolArg(args, "background"),
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
			e.directIPRuntime.IssuePermit(tc.Identity.SessionID, tc.Identity.ToolCallID, actionDigest, requestDigest, confineDigest)
		}
		e.emitDirectIPLifecycle(tools.DirectIPLifecycleEvent{
			Phase:                tools.DirectIPLifecycleApproved,
			SessionID:            tc.Identity.SessionID,
			ToolCallID:           tc.Identity.ToolCallID,
			ActionDigest:         actionDigest,
			AuthorizationSource:  authzledger.AuthorizationSourceNeverAsk,
			DeclaredDestinations: declared,
			Background:           capabilityrequest.BoolArg(args, "background"),
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
		pre, err := e.Approvals.evaluatePreSpawn(ctx, action)
		if err != nil {
			return nil, err
		}
		if pre != nil && pre.Denied {
			return nil, e.Approvals.rejectBoundaryPolicyDeny(ctx, tool, args, tc, pre)
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
		return nil, e.Approvals.rejectApprovalErr(ctx, tool, tc.Identity.Agent, args, err)
	}
	authorized := e.directIPRuntime != nil && e.directIPRuntime.Authorized(tc.Identity.SessionID, tc.Identity.ToolCallID, actionDigest)
	if !authorized {
		return nil, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, &toolrejection.ToolReject{
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
func (e *Capabilities) directIPLeaseAuthorizes(action hitl.ProposedAction, tc tools.ToolContext, lease hitl.DirectIPLease) bool {
	if e == nil || e.directIPRuntime == nil || !lease.Complete() {
		return false
	}
	if !e.directIPRuntime.LeaseCovers(action.ChatSession(), lease) {
		return false
	}
	e.directIPRuntime.IssuePermit(tc.Identity.SessionID, tc.Identity.ToolCallID, lease.ActionDigest, lease.RequestDigest, lease.ConfinementDigest)
	e.emitDirectIPLifecycle(tools.DirectIPLifecycleEvent{
		Phase:                tools.DirectIPLifecycleLeaseReused,
		SessionID:            action.SessionID,
		ToolCallID:           tc.Identity.ToolCallID,
		ActionDigest:         lease.ActionDigest,
		AuthorizationSource:  authzledger.AuthorizationSourceLease,
		DeclaredDestinations: lease.DeclaredDestinations,
		Background:           capabilityrequest.BoolArg(action.Args, "background"),
	})
	return true
}

func (e *Capabilities) awaitDirectIPCapability(
	ctx context.Context,
	action hitl.ProposedAction,
	lease hitl.DirectIPLease,
	tc tools.ToolContext,
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
		ladder = capabilitygrants.DirectIPExecutionGrantOffers(action, lease)
		grantOffers = append(append(grantOffers, ladder...), e.Approvals.absorbedGrantOffers(action, approval)...)
		title = "Run with direct network access"
	}
	permit := hitl.ApprovalAuthorityDelta{
		Kind: hitl.AuthorityDirectIPPermit, SessionID: action.SessionID, ToolCallID: tc.Identity.ToolCallID,
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
	options = append(options, e.Approvals.quietOptionsFor(action, approvalDecision(approval))...)
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
		return toolrejection.ApprovalPlanInvalid()
	}
	permission, err := e.Secrets.prepareSecretPermission(ctx, action.Tool, action.Args, tc)
	if err != nil {
		return err
	}
	plan, err = hitl.ComposeSecretPermission(plan, action, permission, hitl.FaceContext{})
	if err != nil {
		return toolrejection.ApprovalPlanInvalid()
	}
	final, err := e.Approvals.raiseAndWaitToolApproval(ctx, toolApprovalRaise{
		Action:                 action,
		Plan:                   plan,
		SecretScreenHit:        permission != nil,
		Title:                  title,
		ToolCallID:             tc.Identity.ToolCallID,
		ProjectID:              tc.Identity.ProjectID,
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
		e.emitDirectIPLifecycle(tools.DirectIPLifecycleEvent{
			Phase:                tools.DirectIPLifecycleApproved,
			SessionID:            action.SessionID,
			ToolCallID:           tc.Identity.ToolCallID,
			ActionDigest:         actionDigest,
			AuthorizationSource:  authzledger.AuthorizationSourceHuman,
			DeclaredDestinations: declared,
		})
		return nil
	case final.Status == hitl.DecisionStatusRejected || final.Status == hitl.DecisionStatusApproved:
		e.emitDirectIPLifecycle(tools.DirectIPLifecycleEvent{
			Phase:                tools.DirectIPLifecycleDenied,
			SessionID:            action.SessionID,
			ToolCallID:           tc.Identity.ToolCallID,
			ActionDigest:         actionDigest,
			AuthorizationSource:  authzledger.AuthorizationSourceHuman,
			DeclaredDestinations: declared,
		})
		return isolationCheckpointReject(isolation.CodeDirectIPDenied, final)
	case final.Status == hitl.DecisionStatusExpired:
		e.emitDirectIPLifecycle(tools.DirectIPLifecycleEvent{
			Phase:                tools.DirectIPLifecycleDenied,
			SessionID:            action.SessionID,
			ToolCallID:           tc.Identity.ToolCallID,
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
		e.emitDirectIPLifecycle(tools.DirectIPLifecycleEvent{
			Phase:                tools.DirectIPLifecycleDenied,
			SessionID:            action.SessionID,
			ToolCallID:           tc.Identity.ToolCallID,
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

func directIPVisibility(requested bool) string {
	if requested {
		return hitl.DirectIPVisibilityUnobserved
	}
	return "none"
}
