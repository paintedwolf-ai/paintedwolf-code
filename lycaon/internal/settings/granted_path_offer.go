package settings

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/pkg/api"
)

// GrantedPathOffers builds the reuse ladder for a filesystem crossing.
func GrantedPathOffers(action hitl.ProposedAction, target gate.FileTarget, decision *gate.Decision, locations *sensitivepath.Catalog) []hitl.ApprovalGrantOffer {
	var offers []hitl.ApprovalGrantOffer
	for _, access := range grantedPathCandidates(target, locations) {
		offers = append(offers, grantedPathOffersForAccess(action, target, decision, access)...)
	}
	return offers
}

func grantedPathOffersForAccess(action hitl.ProposedAction, target gate.FileTarget, decision *gate.Decision, access hitl.GrantedPathDelta) []hitl.ApprovalGrantOffer {
	ceiling := decision.Reuse().Scope
	if ceiling == gate.ScopeNone || strings.TrimSpace(target.Path) == "" {
		return nil
	}
	for _, fired := range decision.Gates() {
		if fired == api.GateSecretOutbound {
			return nil
		}
	}
	abs := filepath.Clean(strings.TrimSpace(target.Path))
	if !filepath.IsAbs(abs) {
		return nil
	}
	coverage := grantedPathCoverage(access)
	reaskWhen := hitl.ReaskWhenDifferentPath
	if access.Tree {
		reaskWhen = hitl.ReaskWhenOutsideFolder
	}
	type rungSpec struct {
		rung    hitl.ApprovalOptionRung
		scope   hitl.ApprovalGrantScope
		ttl     time.Duration
		title   string
		expires string
	}
	// Day scope depends on the decision ceiling and project identity.
	dayScope := dayCarrierScope(decision.Reuse(), action)
	dayExpires := hitl.ExpiresIn1DayOrRevoked
	if dayScope == hitl.ApprovalGrantScopeChat {
		dayExpires = hitl.ExpiresIn1DayOrChatDeleted
	}
	// The durable option uses the widest allowed scope and stays visible when disabled.
	durable := rungSpec{hitl.ApprovalRungProject, hitl.ApprovalGrantScopeProject, 7 * 24 * time.Hour, hitl.TitleAllowForThisProject, hitl.ExpiresIn7DaysOrRevoked}
	if scopeWithin(hitl.ApprovalGrantScopeDevice, ceiling) {
		durable = rungSpec{hitl.ApprovalRungDevice, hitl.ApprovalGrantScopeDevice, 30 * 24 * time.Hour, hitl.TitleAllowOnThisDevice, hitl.ExpiresIn30DaysOrRevoked}
	}
	rungs := []rungSpec{
		{hitl.ApprovalRungDay, dayScope, time.Duration(hitl.DayRungTTLSeconds) * time.Second, hitl.TitleAllowFor1Day, dayExpires},
		{hitl.ApprovalRungChat, hitl.ApprovalGrantScopeChat, 0, hitl.TitleAllowForThisChat, hitl.ExpiresWhenChatDeleted},
		durable,
	}

	offers := make([]hitl.ApprovalGrantOffer, 0, len(rungs))
	for _, rung := range rungs {
		note := ""
		switch {
		case rung.scope == hitl.ApprovalGrantScopeChat:
		case !action.Scope.HasProjectIdentity():
			note = hitl.NoteNoProjectOpen
		case !scopeWithin(rung.scope, ceiling):
			note = hitl.NoteEndsWithChat
		}
		if note != "" && rung.rung == hitl.ApprovalRungDay {
			// A day grant capped at chat scope also ends when the chat is deleted.
			rung.scope, rung.expires, note = hitl.ApprovalGrantScopeChat, hitl.ExpiresIn1DayOrChatDeleted, ""
		}
		var expiresAt *time.Time
		ttlSeconds := 0
		if rung.ttl > 0 && rung.rung == hitl.ApprovalRungDay {
			ttlSeconds = int(rung.ttl / time.Second)
		} else if rung.ttl > 0 {
			expires := time.Now().UTC().Add(rung.ttl)
			expiresAt = &expires
		}
		rungCoverage := coverage
		if rung.rung == hitl.ApprovalRungDevice {
			rungCoverage = coverage + hitl.DeviceCoverageSuffix
		}
		grant := hitl.ApprovalGrant{
			ID:            grantedPathGrantID(action, access, rung.scope, rung.rung),
			Scope:         rung.scope,
			Predicate:     hitl.ApprovalGrantPredicate{Category: string(ApprovalCategoryPath), Pattern: access.Path},
			ChatSessionID: action.Scope.ChatSession(),
			ProjectID:     action.Scope.ProjectID,
			ProjectDir:    action.Scope.ProjectDir,
			Title:         rung.title,
			Coverage:      rungCoverage,
			GrantedAt:     time.Now().UTC(),
			ExpiresAt:     expiresAt,
			TTLSeconds:    ttlSeconds,
			ExpiresWhen:   rung.expires,
			ReaskWhen:     reaskWhen,
			Source:        "checkpoint",
			GrantedPath:   &access,
		}
		accessCopy := access
		grantCopy := grant
		authority := []hitl.ApprovalAuthorityDelta{{
			Kind:          hitl.AuthorityGrantedPath,
			Grant:         &grantCopy,
			ChatSessionID: action.Scope.ChatSession(),
			GrantedPath:   &accessCopy,
		}, {
			Kind: hitl.AuthorityGenericGrant, Grant: &grantCopy,
		}}
		offer := hitl.ApprovalGrantOffer{
			ID: grant.ID, Rung: rung.rung, Scope: rung.scope, Title: rung.title, Coverage: rungCoverage,
			ExpiresWhen: rung.expires, ReaskWhen: reaskWhen, TTLSeconds: ttlSeconds,
			Subject: gate.ReusePredicate, Grant: grant, Authority: authority,
		}
		if access.Tree {
			offer.DirectoryScope = access.Path
		}
		if note != "" {
			offer = hitl.DisabledOffer(offer, note)
		}
		offers = append(offers, offer)
	}
	return offers
}

func grantedPathAccess(target gate.FileTarget, locations *sensitivepath.Catalog) hitl.GrantedPathDelta {
	abs := filepath.Clean(strings.TrimSpace(target.Path))
	write := target.Mode == gate.ModeWrite
	exact := hitl.GrantedPathDelta{Path: abs, Write: write}
	if write || target.ProtectedSubject || target.Sensitive {
		return exact
	}
	// Canonical folders prevent alias-based tree grants.
	folder, exists, isDir := probeTargetFolder(abs)
	if target.Exists != nil && !*target.Exists && filepath.IsAbs(abs) {
		// The card already says the target is missing; a tree rooted at a
		// missing folder would cover nothing and mint a dead lease.
		exists, isDir = false, false
	}
	if !exists || !isDir {
		folder = filepath.Dir(folder)
		// A tree grant needs a folder that actually exists to root at.
		if _, parentExists, parentIsDir := probeTargetFolder(folder); !parentExists || !parentIsDir {
			return exact
		}
	}
	if folder == "" || folder == "." {
		return exact
	}
	if refused, code := confine.AttachedWriteRootRefused(folder); refused && code != confine.WriteRootCodeContainsState {
		return exact
	}
	if locations != nil {
		if _, ok := locations.Match(folder, sensitivepath.ModeRead); ok {
			return exact
		}
	}
	return hitl.GrantedPathDelta{Path: folder, Tree: true}
}

// probeTargetFolder stats the canonical spelling of a declared target. The
// card's existence fact and the grant-shape minting read the same observation,
// so a missing path is disclosed and granted from one probe.
func probeTargetFolder(path string) (folder string, exists, isDir bool) {
	folder = filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(strings.TrimSpace(path))))
	info, err := os.Lstat(folder)
	return folder, err == nil, err == nil && info.IsDir()
}

func grantedPathCoverage(access hitl.GrantedPathDelta) string {
	if access.Write {
		return hitl.CoverageWritesTo(access.Path)
	}
	if access.Tree {
		return hitl.CoverageReadsOfTree(access.Path)
	}
	return hitl.CoverageReadsOf(access.Path)
}

func grantedPathGrantID(
	action hitl.ProposedAction, access hitl.GrantedPathDelta,
	scope hitl.ApprovalGrantScope, rung hitl.ApprovalOptionRung,
) string {
	mode := "read"
	if access.Write {
		mode = "write"
	}
	shape := "exact"
	if access.Tree {
		shape = "tree"
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		string(scope), string(rung), "granted_path", access.Path, mode, shape,
		action.Scope.ChatSession(), action.Scope.ProjectID,
	}, "\x00")))
	return "grant_" + hex.EncodeToString(sum[:8])
}

// grantedPathCandidates orders canonical read scopes from the target folder outward.
func grantedPathCandidates(target gate.FileTarget, locations *sensitivepath.Catalog) []hitl.GrantedPathDelta {
	access := grantedPathAccess(target, locations)
	out := []hitl.GrantedPathDelta{access}
	if !access.Tree {
		return out
	}
	for dir := filepath.Dir(access.Path); dir != access.Path; dir = filepath.Dir(dir) {
		if refused, code := confine.AttachedWriteRootRefused(dir); refused && code != confine.WriteRootCodeContainsState {
			break
		}
		if locations != nil {
			if _, sensitive := locations.Match(dir, sensitivepath.ModeRead); sensitive {
				break
			}
		}
		// Every offered tree roots at a folder that exists; a lease over a
		// missing folder would silently widen once something creates it.
		if _, exists, isDir := probeTargetFolder(dir); !exists || !isDir {
			continue
		}
		out = append(out, hitl.GrantedPathDelta{Path: dir, Tree: true})
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return out
}
