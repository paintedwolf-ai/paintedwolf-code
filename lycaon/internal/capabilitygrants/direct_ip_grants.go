package capabilitygrants

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
)

// DirectIPExecutionGrantOffers is the execution card's day-then-chat ladder.
// Direct IP has no durable rung: the broker cannot describe where an
// unobserved command reaches, so the project slot stays in place, disabled.
func DirectIPExecutionGrantOffers(action hitl.ProposedAction, lease hitl.DirectIPLease) []hitl.ApprovalGrantOffer {
	if !lease.Complete() {
		return nil
	}
	task := directIPChatGrantOffer(action, lease)
	day := hitl.DayRung(task)
	task.Authority = directIPChatAuthority(action, lease, &task.Grant, 0)
	day.Authority = directIPChatAuthority(action, lease, &day.Grant, day.TTLSeconds)
	project := task
	project.ID = task.ID + "-project"
	project.Grant.ID = project.ID
	project.Rung = hitl.ApprovalRungProject
	project.Scope = hitl.ApprovalGrantScopeProject
	project.Grant.Scope = hitl.ApprovalGrantScopeProject
	project.Title = hitl.TitleAllowForThisProject
	project.Grant.Title = project.Title
	project.ExpiresWhen = hitl.ExpiresIn7DaysOrRevoked
	project.Grant.ExpiresWhen = project.ExpiresWhen
	project.Authority = directIPChatAuthority(action, lease, &project.Grant, 0)
	return []hitl.ApprovalGrantOffer{day, task, hitl.DisabledOffer(project, hitl.NoteEndsWithChat)}
}

func directIPChatAuthority(
	action hitl.ProposedAction,
	lease hitl.DirectIPLease,
	grant *hitl.ApprovalGrant,
	ttlSeconds int,
) []hitl.ApprovalAuthorityDelta {
	return []hitl.ApprovalAuthorityDelta{{
		Kind: hitl.AuthorityDirectIPChat, Grant: grant, ChatSessionID: action.ChatSession(),
		DirectIPLease: &lease, TTLSeconds: ttlSeconds,
	}}
}

func directIPChatGrantOffer(action hitl.ProposedAction, lease hitl.DirectIPLease) hitl.ApprovalGrantOffer {
	chatConfinement := lease.ChatConfinementDigest
	if chatConfinement == "" {
		chatConfinement = hitl.DirectIPChatConfinementDigest(action.Contained.Roots, action.Contained.Egress)
	}
	grant := hitl.ApprovalGrant{
		Scope: hitl.ApprovalGrantScopeChat,
		// The predicate pattern is the chat confinement digest, not the command text or exact triple:
		// a chat lease covers direct network access for any command under the same confinement roots.
		Predicate:     hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryDirectIP, Pattern: chatConfinement},
		ChatSessionID: action.ChatSession(),
		ProjectID:     action.ProjectID,
		ProjectDir:    action.ProjectDir,
		Title:         hitl.TitleAllowForThisChat,
		Coverage:      directIPCoverage(lease),
		ExpiresWhen:   hitl.ExpiresWhenChatDeleted,
		ReaskWhen:     "the confinement roots change",
		Witness:       hitl.BoundaryWitness(action.Contained),
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		string(hitl.ApprovalGrantScopeChat),
		hitl.ApprovalGrantCategoryDirectIP,
		chatConfinement,
		action.ChatSession(),
	}, "\x00")))
	grant.ID = "grant_" + hex.EncodeToString(sum[:8])
	return hitl.ApprovalGrantOffer{
		ID: grant.ID, Rung: hitl.ApprovalRungChat, Scope: grant.Scope, Title: grant.Title, Coverage: grant.Coverage,
		ExpiresWhen: grant.ExpiresWhen, ReaskWhen: grant.ReaskWhen, Subject: gate.ReuseExactAction, Grant: grant,
	}
}

// directIPCoverage names the scope.
func directIPCoverage(lease hitl.DirectIPLease) string {
	return "direct network access to any destination for commands in this chat under these project roots"
}
