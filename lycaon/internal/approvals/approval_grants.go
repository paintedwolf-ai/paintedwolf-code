package approvals

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// ApprovalGrants maps active host authority to the revoke-management wire shape.
func ApprovalGrants(grants []hitl.ApprovalGrant) []wire.ApprovalGrant {
	now := time.Now().UTC()
	out := make([]wire.ApprovalGrant, 0, len(grants))
	for _, grant := range grants {
		grantedAt := grant.GrantedAt.UTC()
		row := wire.ApprovalGrant{
			ElevatedEffects: hitl.ElevatedGrantEffects(grant),
			ID:              grant.ID, Scope: wire.ApprovalGrantScope(grant.Scope),
			Category: wire.ApprovalGrantCategory(grant.Predicate.Category), Pattern: grant.Predicate.Pattern,
			Title: grant.Title, Coverage: grant.Coverage, GrantedAt: grantedAt,
			ExpiresWhen: grant.ExpiresWhen, ReaskWhen: grant.ReaskWhen,
			ProjectID: grant.ProjectID, ProjectDir: grant.ProjectDir, ChatSessionID: grant.ChatSessionID,
			Source: grant.Source,
		}
		if grant.ExpiresAt != nil {
			expires := grant.ExpiresAt.UTC()
			row.ExpiresAt = &expires
			if !expires.After(now) {
				row.Expired = true
			}
		}
		if len(grant.ExactActionSet) > 0 {
			row.ActionCount = len(grant.ExactActionSet)
		}
		if grant.Predicate.Category == string(settings.ApprovalCategoryHostResource) {
			row.ResourceIDs = splitResourceIDs(grant.Predicate.Pattern)
		}
		if grant.Predicate.Category == string(settings.ApprovalCategorySecret) {
			row.Pattern = secretGrantPattern(len(grant.SecretFingerprints))
			row.SecretNames = append([]string(nil), grant.SecretNames...)
			row.SecretRecipients = hitl.WireSecretRecipients(grant.SecretRecipients)
		}
		if grant.Predicate.Category == string(settings.ApprovalCategoryWriteRoot) {
			if _, err := os.Lstat(grant.Predicate.Pattern); err != nil {
				row.Unavailable = true
			}
		}
		if grant.Predicate.Category == string(settings.ApprovalCategorySocketPath) ||
			grant.Predicate.Category == hitl.ApprovalGrantCategorySocketCapability {
			annotateSocketGrant(&row, grant)
		}
		out = append(out, row)
	}
	return out
}

func secretGrantPattern(count int) string {
	if count == 1 {
		return "1 detected secret"
	}
	return fmt.Sprintf("%d detected secrets", count)
}

func annotateSocketGrant(row *wire.ApprovalGrant, grant hitl.ApprovalGrant) {
	row.Category = wire.ApprovalGrantCategorySocketPath
	approved := strings.TrimSpace(grant.ApprovedPath)
	resolved := strings.TrimSpace(grant.ResolvedPath)
	if approved == "" {
		approved = strings.TrimSpace(grant.Predicate.Pattern)
	}
	if resolved == "" {
		resolved = approved
	}
	row.ApprovedPath = approved
	row.ResolvedPath = resolved
	if row.Pattern == "" {
		row.Pattern = approved
	}
	row.EffectiveAuthority = wire.SocketCapabilityAuthorityOutsideSandboxDaemon
	row.AuthorityWarning = wire.SocketGrantAuthorityWarning
	row.RevokeAppliesTo = wire.SocketGrantRevokeAppliesTo
	if row.Source == "" {
		row.Source = grant.Source
	}
	if approved == "" {
		row.Unavailable = true
		return
	}
	g := confine.SocketGrant{ApprovedPath: approved, ResolvedPath: resolved}
	if err := confine.RevalidateSocketGrant(g); err == nil {
		return
	}
	now, err := confine.ResolveSocketRequest(approved)
	if err != nil {
		row.Unavailable = true
		return
	}
	if now.ResolvedPath != resolved {
		row.Repointed = true
		row.ResolvedPath = now.ResolvedPath
		return
	}
	row.Unavailable = true
}

func splitResourceIDs(pattern string) []string {
	parts := strings.Split(pattern, ",")
	out := make([]string, 0, len(parts))
	for _, id := range parts {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
