package capabilityadmin

import (
	"context"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleListApprovalGrants(w http.ResponseWriter, r *http.Request) {
	chat := strings.TrimSpace(r.URL.Query().Get("session_id"))
	domainGrants := s.approvalGrants(chat)
	// Preserve first-class socket fields on durable rows.
	grants := approvals.ApprovalGrants(domainGrants)
	s.fillGrantSessionTitles(r.Context(), grants)
	quiets := s.listAskQuiets(r.Context(), chat)
	httpio.WriteJSON(w, http.StatusOK, wire.ApprovalGrantsResponse{Grants: grants, Quiets: quiets})
}

func (s *Handler) listAskQuiets(ctx context.Context, chat string) map[string]wire.AskQuiet {
	domain := s.Gate.ListAskQuiets(chat)
	if len(domain) == 0 {
		return nil
	}
	out := make(map[string]wire.AskQuiet, len(domain))
	titles := map[string]string{}
	for _, q := range domain {
		title := ""
		if q.ChatSessionID != "" {
			if cached, ok := titles[q.ChatSessionID]; ok {
				title = cached
			} else if sess, err := s.Store.Get(ctx, q.ChatSessionID); err == nil && sess != nil {
				title = sess.Title
				titles[q.ChatSessionID] = title
			}
		}
		out[q.ID] = wire.AskQuiet{
			ID: q.ID, ChatSessionID: q.ChatSessionID, Key: q.Key, Label: q.Label,
			ElevatedEffects: q.ElevatedEffects,
			ExpiresAt:       q.ExpiresAt, Suppressed: q.Suppressed, CreatedAt: q.CreatedAt,
			SessionTitle: title,
		}
	}
	return out
}

// fillGrantSessionTitles resolves chat titles for chat-scoped rows so the
// Settings list can group them under their chat.
func (s *Handler) fillGrantSessionTitles(ctx context.Context, grants []wire.ApprovalGrant) {
	titles := map[string]string{}
	for i := range grants {
		id := grants[i].ChatSessionID
		if id == "" || grants[i].Scope != wire.ApprovalGrantScopeChat {
			continue
		}
		title, seen := titles[id]
		if !seen {
			if sess, err := s.Store.Get(ctx, id); err == nil && sess != nil {
				title = sess.Title
			}
			titles[id] = title
		}
		grants[i].SessionTitle = title
	}
}

func socketGrantToDomain(grant settings.ApprovalGrant) hitl.ApprovalGrant {
	return hitl.ApprovalGrant{
		ElevatedEffects: grant.ElevatedEffects,
		ID:              grant.ID, Scope: grant.Scope,
		Predicate: hitl.ApprovalGrantPredicate{Category: string(grant.Category), Pattern: grant.Pattern},
		ProjectID: grant.ProjectID, ProjectDir: grant.ProjectDir, Title: grant.Title, Coverage: grant.Coverage,
		GrantedAt: grant.GrantedAt, ExpiresAt: grant.ExpiresAt, ExpiresWhen: grant.ExpiresWhen,
		ReaskWhen: grant.ReaskWhen, Witness: grant.Witness,
		ApprovedPath: grant.ApprovedPath, ResolvedPath: grant.ResolvedPath, Source: grant.Source,
	}
}

func chatSocketGrantsToDomain(rootSessionID string, grants []approvalstate.SocketChatGrant) []hitl.ApprovalGrant {
	out := make([]hitl.ApprovalGrant, 0, len(grants))
	for _, tg := range grants {
		row := hitl.ApprovalGrant{
			ID:            tg.ID,
			Scope:         hitl.ApprovalGrantScopeChat,
			Predicate:     hitl.ApprovalGrantPredicate{Category: string(settings.ApprovalCategorySocketPath), Pattern: tg.ApprovedPath},
			ChatSessionID: rootSessionID,
			Title:         "Allow local service for this chat",
			Coverage:      "connect to `" + tg.ApprovedPath + "`",
			GrantedAt:     tg.CreatedAt,
			ExpiresWhen:   hitl.ExpiresWhenChatDeleted,
			ReaskWhen:     "the socket target, task, or confinement changes",
			ApprovedPath:  tg.ApprovedPath,
			ResolvedPath:  tg.ResolvedPath,
			Source:        "checkpoint",
		}
		if tg.ExpiresAt != nil {
			expires := *tg.ExpiresAt
			row.ExpiresAt = &expires
			row.Title = "Allow local service for 1 day"
			row.ExpiresWhen = hitl.ExpiresIn1DayOrChatDeleted
		}
		out = append(out, row)
	}
	return out
}

func (s *Handler) approvalGrants(chat string) []hitl.ApprovalGrant {
	domainGrants := s.Gate.ListGrants(chat)
	if s.Sockets != nil {
		if chat != "" {
			domainGrants = append(domainGrants, chatSocketGrantsToDomain(chat, s.Sockets.ListChatGrants(chat))...)
		} else {
			for root, chatGrants := range s.Sockets.ListAllChatGrants() {
				domainGrants = append(domainGrants, chatSocketGrantsToDomain(root, chatGrants)...)
			}
		}
	}
	domainGrants = append(domainGrants, s.directIPGrantsForList(chat)...)
	domainGrants = append(domainGrants, s.writeRootGrantsForList(chat)...)
	domainGrants = append(domainGrants, s.localListenGrantsForList(chat)...)
	domainGrants = append(domainGrants, s.loopbackGrantsForList(chat)...)

	for i := range domainGrants {
		grant := &domainGrants[i]
		if grant.Predicate.Category == string(settings.ApprovalCategoryHostResource) {
			for _, mode := range s.HostResources.ConnectionModes(strings.Split(grant.Predicate.Pattern, ",")) {
				switch mode {
				case hostresources.ConnectionNone, hostresources.ConnectionProxy, hostresources.ConnectionSOCKS,
					hostresources.ConnectionBaselineLoopback, hostresources.ConnectionDynamic:
					continue
				case hostresources.ConnectionLocalService:
					grant.ElevatedEffects = append(grant.ElevatedEffects, wire.ElevatedAccessEffectLocalService)
				case hostresources.ConnectionDirectIP:
					grant.ElevatedEffects = append(grant.ElevatedEffects, wire.ElevatedAccessEffectDirectNetwork)
				}
			}
		}
	}
	return domainGrants
}
