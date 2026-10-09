package toolexecution

import (
	"github.com/lycaon/lycaon/internal/capabilitygrants"

	"crypto/sha256"
	"encoding/hex"
	"github.com/lycaon/lycaon/internal/tools"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
)

// socketPermitDelta is the current-call authority that releases a held socket_set.
func socketPermitDelta(
	action hitl.ProposedAction,
	actionDigest string,
	tc tools.ToolContext,
	targets []hitl.ApprovalSocketTarget,
) hitl.ApprovalAuthorityDelta {
	return hitl.ApprovalAuthorityDelta{
		Kind: hitl.AuthoritySocketPermit, SessionID: action.SessionID, ToolCallID: tc.ToolCallID,
		ActionDigest: actionDigest, Sockets: targets,
	}
}

func socketOnceOption(permit hitl.ApprovalAuthorityDelta) hitl.ApprovalOption {
	return hitl.ApprovalOption{
		ID: "approve_socket_set_once", Kind: hitl.ApprovalOptionCurrentAction, Rung: hitl.ApprovalRungOnce,
		Title: hitl.TitleAllowOnce, Coverage: "only these local services for this action",
		ExpiresWhen: hitl.ExpiresAfterThisAction, ReaskWhen: "a later action requests these services",
		DecisionAction: "approve",
		Authority:      []hitl.ApprovalAuthorityDelta{permit},
	}
}

// socketCapabilityOptions is the execution card: once, then every axis and
// absorbed lease composed with the current-call permit that continues the hold.
func socketCapabilityOptions(
	permit hitl.ApprovalAuthorityDelta,
	axisOffers, absorbed []hitl.ApprovalGrantOffer,
) []hitl.ApprovalOption {
	options := []hitl.ApprovalOption{socketOnceOption(permit)}
	for _, offer := range axisOffers {
		options = append(options, hitl.ContinuingLeaseOption(offer, permit))
	}
	for _, offer := range absorbed {
		options = append(options, hitl.ContinuingLeaseOption(offer, permit))
	}
	return options
}

func combinedDirectIPAuthority(option hitl.ApprovalOption, direct directIPApprovalReview, tc tools.ToolContext) []hitl.ApprovalAuthorityDelta {
	permit := hitl.ApprovalAuthorityDelta{
		Kind: hitl.AuthorityDirectIPPermit, SessionID: direct.Action.SessionID, ToolCallID: tc.ToolCallID,
		ActionDigest: direct.Lease.ActionDigest, DirectIPLease: &direct.Lease,
	}
	// Absorbed second-subject options and once: current-call only.
	if option.Group != "" || option.Kind != hitl.ApprovalOptionLease {
		return []hitl.ApprovalAuthorityDelta{permit}
	}
	for _, offer := range capabilitygrants.DirectIPExecutionGrantOffers(direct.Action, direct.Lease) {
		if offer.Rung != option.Rung {
			continue
		}
		return []hitl.ApprovalAuthorityDelta{{
			Kind: hitl.AuthorityDirectIPChat, Grant: &offer.Grant, ChatSessionID: direct.Action.ChatSession(),
			DirectIPLease: &direct.Lease, TTLSeconds: offer.TTLSeconds,
		}, permit}
	}
	return []hitl.ApprovalAuthorityDelta{permit}
}

// attachRealizationWriteRoots adds catalogued daemon write roots to lease
// options. Once uses the spawn overlay only; the Day rung bounds the roots
// to its 24 hours like the socket lease it rides.
func attachRealizationWriteRoots(options []hitl.ApprovalOption, action hitl.ProposedAction, roots []string) []hitl.ApprovalOption {
	if len(roots) == 0 {
		return options
	}
	grant := realizationWriteRootGrant(action, roots)
	for i := range options {
		if options[i].Kind != hitl.ApprovalOptionLease {
			continue
		}
		delta := hitl.ApprovalAuthorityDelta{
			Kind: hitl.AuthorityWriteRootChat, Grant: &grant,
			ChatSessionID: action.ChatSession(), WriteRoots: append([]string(nil), roots...),
		}
		if options[i].Rung == hitl.ApprovalRungDay {
			delta.TTLSeconds = hitl.DayRungTTLSeconds
		}
		options[i].Authority = append(options[i].Authority, delta)
	}
	return options
}

func realizationWriteRootGrant(action hitl.ProposedAction, roots []string) hitl.ApprovalGrant {
	pattern := strings.Join(roots, "\x1e")
	raw := strings.Join([]string{
		string(hitl.ApprovalGrantScopeChat), hitl.ApprovalGrantCategoryWriteRoot,
		pattern, action.ChatSession(),
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return hitl.ApprovalGrant{
		ID: "grant_" + hex.EncodeToString(sum[:8]), Scope: hitl.ApprovalGrantScopeChat,
		Predicate:     hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryWriteRoot, Pattern: pattern},
		ChatSessionID: action.ChatSession(), ProjectID: action.ProjectID, ProjectDir: action.ProjectDir,
		Title: hitl.TitleAllowForThisChat, Coverage: "writes required by the reviewed local service",
		GrantedAt: time.Now().UTC(), ExpiresWhen: hitl.ExpiresWhenChatDeleted,
		ReaskWhen: "this chat is deleted", Source: "checkpoint",
	}
}
