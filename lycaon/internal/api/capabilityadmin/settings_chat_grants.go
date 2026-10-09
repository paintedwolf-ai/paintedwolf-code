package capabilityadmin

import (
	"context"

	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
)

// directIPGrantsForList returns the direct-IP lease rows for the Settings list, for one
// chat or for every live session tree when chat is empty.
func (s *Inventory) directIPGrantsForList(chat string) []hitl.ApprovalGrant {
	if s == nil || s.DirectIP == nil {
		return nil
	}
	if chat != "" {
		return chatDirectIPGrantsToDomain(chat, s.DirectIP.ListChatGrants(chat))
	}
	var out []hitl.ApprovalGrant
	for root, grants := range s.DirectIP.ListAllChatGrants() {
		out = append(out, chatDirectIPGrantsToDomain(root, grants)...)
	}
	return out
}

func chatDirectIPGrantsToDomain(rootSessionID string, grants []approvalstate.DirectIPChatGrant) []hitl.ApprovalGrant {
	out := make([]hitl.ApprovalGrant, 0, len(grants))
	for _, tg := range grants {
		// The lease covers the command; no destination was observed.
		pattern := tg.CommandSummary
		if pattern == "" {
			pattern = tg.ActionDigest
		}
		row := hitl.ApprovalGrant{
			ID:            tg.ID,
			Scope:         hitl.ApprovalGrantScopeChat,
			Predicate:     hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryDirectIP, Pattern: pattern},
			ChatSessionID: rootSessionID,
			Title:         "Allow direct network for this chat",
			Coverage:      "`" + pattern + "`, reaching the network unobserved",
			GrantedAt:     tg.CreatedAt,
			ExpiresWhen:   hitl.ExpiresWhenChatDeleted,
			ReaskWhen:     "the command, its declared destinations, or the confinement changes",
			Source:        "checkpoint",
		}
		if tg.ExpiresAt != nil {
			expires := *tg.ExpiresAt
			row.ExpiresAt = &expires
			row.Title = "Allow direct network for 1 day"
			row.ExpiresWhen = hitl.ExpiresIn1DayOrChatDeleted
		}
		out = append(out, row)
	}
	return out
}

// revokeDirectIPGrant removes a direct-IP lease and records the capability row.
// Reports whether the id belonged to this runtime.
func (s *Inventory) revokeDirectIPGrant(ctx context.Context, id string) bool {
	if s == nil || s.DirectIP == nil {
		return false
	}
	tg, root, ok := s.DirectIP.FindByID(id)
	if !ok {
		return false
	}
	s.DirectIP.RevokeByID(id)
	if s.AuthzRecorder != nil {
		_ = s.AuthzRecorder.AppendCapabilityRecord(ctx, authzledger.CapabilityRecord{
			SessionID:            root,
			Action:               authzledger.ActionCapabilityRevoked,
			Outcome:              authzledger.OutcomeAllowed,
			ResolvedBy:           authzledger.ResolvedByHuman,
			ResolverPersonID:     requestscope.ContextCaller(ctx).ID,
			Tool:                 "settings",
			AuthorizationSource:  authzledger.AuthorizationSourceHuman,
			Direct:               true,
			DeclaredDestinations: append([]string(nil), tg.DeclaredDestinations...),
		})
	}
	return true
}

func (s *Inventory) localListenGrantsForList(chat string) []hitl.ApprovalGrant {
	if s == nil || s.Listen == nil {
		return nil
	}
	if chat != "" {
		return chatListenGrantsToDomain(chat, s.Listen.ListChatGrants(chat))
	}
	var out []hitl.ApprovalGrant
	for root, grants := range s.Listen.ListAllChatGrants() {
		out = append(out, chatListenGrantsToDomain(root, grants)...)
	}
	return out
}

// portLeaseGrantsToDomain projects one capability's port leases. Category and
// coverage verb are all that separate binding a port from connecting to one.
func portLeaseGrantsToDomain(
	rootSessionID, category, coverageVerb string,
	grants []approvalstate.PortLeaseChatGrant,
) []hitl.ApprovalGrant {
	out := make([]hitl.ApprovalGrant, 0, len(grants))
	for _, grant := range grants {
		out = append(out, hitl.ApprovalGrant{
			ID: grant.ID, Scope: hitl.ApprovalGrantScopeChat,
			Predicate: hitl.ApprovalGrantPredicate{
				Category: category,
				Pattern:  approvalstate.PortGrantKey(grant.Ports),
			},
			ChatSessionID: rootSessionID,
			Title:         hitl.TitleAllowForThisChat,
			Coverage:      coverageVerb + " " + confine.ListenPortsLabel(grant.Ports),
			GrantedAt:     grant.CreatedAt,
			ExpiresWhen:   hitl.ExpiresWhenChatDeleted,
			ReaskWhen:     "a port outside this grant is needed",
			Source:        "checkpoint",
		})
	}
	return out
}

func chatListenGrantsToDomain(rootSessionID string, grants []approvalstate.PortLeaseChatGrant) []hitl.ApprovalGrant {
	return portLeaseGrantsToDomain(rootSessionID,
		hitl.ApprovalGrantCategoryLocalListen, "binding", grants)
}

func (s *Inventory) loopbackGrantsForList(chat string) []hitl.ApprovalGrant {
	if s == nil || s.Loopback == nil {
		return nil
	}
	if chat != "" {
		return chatLoopbackGrantsToDomain(chat, s.Loopback.ListChatGrants(chat))
	}
	var out []hitl.ApprovalGrant
	for root, grants := range s.Loopback.ListAllChatGrants() {
		out = append(out, chatLoopbackGrantsToDomain(root, grants)...)
	}
	return out
}

func chatLoopbackGrantsToDomain(rootSessionID string, grants []approvalstate.PortLeaseChatGrant) []hitl.ApprovalGrant {
	return portLeaseGrantsToDomain(rootSessionID,
		hitl.ApprovalGrantCategoryLoopbackConnect, "connecting to", grants)
}

func (s *Inventory) writeRootGrantsForList(chat string) []hitl.ApprovalGrant {
	if s == nil || s.WriteRoots == nil {
		return nil
	}
	if chat != "" {
		return chatWriteRootGrantsToDomain(chat, s.WriteRoots.ListChatGrants(chat))
	}
	var out []hitl.ApprovalGrant
	for root, grants := range s.WriteRoots.ListAllChatGrants() {
		out = append(out, chatWriteRootGrantsToDomain(root, grants)...)
	}
	return out
}

func chatWriteRootGrantsToDomain(rootSessionID string, grants []approvalstate.PathChatGrant) []hitl.ApprovalGrant {
	out := make([]hitl.ApprovalGrant, 0, len(grants))
	for _, grant := range grants {
		row := hitl.ApprovalGrant{
			ID: grant.ID, Scope: hitl.ApprovalGrantScopeChat,
			Predicate: hitl.ApprovalGrantPredicate{
				Category: hitl.ApprovalGrantCategoryWriteRoot,
				Pattern:  grant.Root,
			},
			ChatSessionID: rootSessionID,
			Title:         hitl.TitleAllowForThisChat,
			Coverage:      "writes within `" + grant.Root + "`",
			GrantedAt:     grant.CreatedAt,
			ExpiresWhen:   hitl.ExpiresWhenChatDeleted,
			ReaskWhen:     "a different write root is needed",
			Source:        "checkpoint",
		}
		if grant.ExpiresAt != nil {
			expires := *grant.ExpiresAt
			row.ExpiresAt = &expires
			row.Title = hitl.TitleAllowFor1Day
			row.ExpiresWhen = hitl.ExpiresIn1DayOrChatDeleted
		}
		out = append(out, row)
	}
	return out
}
