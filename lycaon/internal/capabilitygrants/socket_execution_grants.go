package capabilitygrants

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/pkg/api"
)

// SocketExecutionGrantOffers builds the day, chat, and project socket leases.
func SocketExecutionGrantOffers(action hitl.ProposedAction, grants []confine.SocketGrant) []hitl.ApprovalGrantOffer {
	task := socketChatGrantOffer(action, grants)
	day := task
	if gate.ReuseFor(api.GateUnobservedChannel).DayScope() == gate.ScopeProject &&
		action.HasProjectIdentity() {
		day = socketProjectDayCarrier(task, action)
	}
	day = hitl.DayRung(day)
	task.Authority = socketChatGrantAuthority(action, grants, &task.Grant, 0)
	if day.Scope == hitl.ApprovalGrantScopeChat {
		day.Authority = socketChatGrantAuthority(action, grants, &day.Grant, day.TTLSeconds)
	} else {
		day.Authority = socketProjectAuthorities(action, grants, day)
	}
	project := socketProjectGrantOffer(task, action, grants)
	return []hitl.ApprovalGrantOffer{day, task, project}
}

// socketProjectDayCarrier lets the day lease survive chat deletion.
func socketProjectDayCarrier(task hitl.ApprovalGrantOffer, action hitl.ProposedAction) hitl.ApprovalGrantOffer {
	day := task
	day.Authority = nil
	day.Scope = hitl.ApprovalGrantScopeProject
	day.Grant.Scope = hitl.ApprovalGrantScopeProject
	day.Grant.ChatSessionID = ""
	day.Grant.ProjectID = action.ProjectID
	day.Grant.ProjectDir = action.ProjectDir
	sum := sha256.Sum256([]byte(strings.Join([]string{
		string(hitl.ApprovalGrantScopeProject),
		hitl.ApprovalGrantCategorySocketCapability,
		task.Grant.Predicate.Pattern,
		action.ProjectID,
	}, "\x00")))
	day.ID = "grant_" + hex.EncodeToString(sum[:8])
	day.Grant.ID = day.ID
	return day
}

func socketChatGrantAuthority(
	action hitl.ProposedAction,
	grants []confine.SocketGrant,
	grant *hitl.ApprovalGrant,
	ttlSeconds int,
) []hitl.ApprovalAuthorityDelta {
	targets := make([]hitl.ApprovalSocketTarget, 0, len(grants))
	for _, socket := range grants {
		targets = append(targets, hitl.ApprovalSocketTarget{
			ApprovedPath: socket.ApprovedPath,
			ResolvedPath: socket.ResolvedPath,
		})
	}
	return []hitl.ApprovalAuthorityDelta{{
		Kind:          hitl.AuthoritySocketChat,
		Grant:         grant,
		ChatSessionID: action.ChatSession(),
		ActionDigest:  hitl.GrantKey(action),
		Sockets:       targets,
		TTLSeconds:    ttlSeconds,
	}}
}

func socketProjectGrantOffer(task hitl.ApprovalGrantOffer, action hitl.ProposedAction, grants []confine.SocketGrant) hitl.ApprovalGrantOffer {
	project := task
	project.Rung = hitl.ApprovalRungProject
	project.Scope = hitl.ApprovalGrantScopeProject
	project.Group = ""
	project.Authority = nil
	project.Grant.Scope = hitl.ApprovalGrantScopeProject
	project.Grant.ChatSessionID = ""
	project.Grant.ProjectID = action.ProjectID
	project.Grant.ProjectDir = action.ProjectDir
	project.Title = hitl.TitleAllowForThisProject
	project.ExpiresWhen = hitl.ExpiresIn7DaysOrRevoked
	now := project.Grant.GrantedAt
	expires := now.Add(hitl.ProjectLeaseDuration)
	project.Grant.ExpiresAt = &expires
	project.Grant.Title = project.Title
	project.Grant.ExpiresWhen = project.ExpiresWhen
	sum := sha256.Sum256([]byte(strings.Join([]string{
		string(hitl.ApprovalGrantScopeProject),
		hitl.ApprovalGrantCategorySocketCapability,
		task.Grant.Predicate.Pattern,
		action.ProjectID,
	}, "\x00")))
	project.ID = "grant_" + hex.EncodeToString(sum[:8])
	project.Grant.ID = project.ID
	project.Authority = socketProjectAuthorities(action, grants, project)
	if !action.HasProjectIdentity() {
		project = hitl.DisabledOffer(project, hitl.NoteNoProjectOpen)
	}
	return project
}

func socketProjectAuthorities(
	action hitl.ProposedAction,
	grants []confine.SocketGrant,
	offer hitl.ApprovalGrantOffer,
) []hitl.ApprovalAuthorityDelta {
	authority := make([]hitl.ApprovalAuthorityDelta, 0, len(grants))
	for _, socket := range grants {
		sum := sha256.Sum256([]byte(strings.Join([]string{
			string(hitl.ApprovalGrantScopeProject),
			hitl.ApprovalGrantCategorySocketPath,
			socket.ApprovedPath,
			socket.ResolvedPath,
			action.ProjectID,
			action.ProjectDir,
		}, "\x00")))
		grant := hitl.ApprovalGrant{
			ID:           "grant_" + hex.EncodeToString(sum[:8]),
			Scope:        hitl.ApprovalGrantScopeProject,
			Predicate:    hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategorySocketPath, Pattern: socket.ApprovedPath},
			ProjectID:    action.ProjectID,
			ProjectDir:   action.ProjectDir,
			Title:        offer.Title,
			Coverage:     "connect to `" + socket.ApprovedPath + "`",
			GrantedAt:    offer.Grant.GrantedAt,
			ExpiresAt:    offer.Grant.ExpiresAt,
			TTLSeconds:   offer.TTLSeconds,
			ExpiresWhen:  offer.ExpiresWhen,
			ReaskWhen:    offer.ReaskWhen,
			Witness:      offer.Grant.Witness,
			ApprovedPath: socket.ApprovedPath,
			ResolvedPath: socket.ResolvedPath,
			Source:       "checkpoint",
		}
		authority = append(authority, hitl.ApprovalAuthorityDelta{
			Kind: hitl.AuthorityGenericGrant, Grant: &grant, TTLSeconds: offer.TTLSeconds,
		})
	}
	return authority
}

func socketChatGrantOffer(action hitl.ProposedAction, grants []confine.SocketGrant) hitl.ApprovalGrantOffer {
	now := time.Now().UTC()
	domainGrant := hitl.ApprovalGrant{
		Scope:         hitl.ApprovalGrantScopeChat,
		Predicate:     hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategorySocketCapability, Pattern: confine.SocketPathsDigest(grants)},
		ChatSessionID: action.ChatSession(),
		ProjectID:     action.ProjectID,
		ProjectDir:    action.ProjectDir,
		Title:         hitl.TitleAllowForThisChat,
		Coverage:      "connections to this exact local-service set",
		GrantedAt:     now,
		ExpiresWhen:   hitl.ExpiresWhenChatDeleted,
		ReaskWhen:     "the socket path, symlink target, or confinement changes",
		Witness:       hitl.BoundaryWitness(action.Contained),
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		string(hitl.ApprovalGrantScopeChat),
		hitl.ApprovalGrantCategorySocketCapability,
		confine.SocketPathsDigest(grants),
		action.ChatSession(),
	}, "\x00")))
	domainGrant.ID = "grant_" + hex.EncodeToString(sum[:8])
	return hitl.ApprovalGrantOffer{
		ID: domainGrant.ID, Rung: hitl.ApprovalRungChat, Scope: domainGrant.Scope, Title: domainGrant.Title,
		Coverage: domainGrant.Coverage, ExpiresWhen: domainGrant.ExpiresWhen,
		ReaskWhen: domainGrant.ReaskWhen, Subject: gate.ReuseExactAction, Grant: domainGrant,
	}
}

func SocketSetCoalesceKey(action hitl.ProposedAction, grants []confine.SocketGrant) string {
	key := hitl.GrantKey(action)
	if key == "" {
		return ""
	}
	return key + "\x00" + confine.SocketPathsDigest(grants)
}

func SocketGrantPairKey(g confine.SocketGrant) string {
	g = NormalizeToolSocketGrant(g)
	return g.ApprovedPath + "\x00" + g.ResolvedPath
}

func NormalizeToolSocketGrant(g confine.SocketGrant) confine.SocketGrant {
	return confine.SocketGrant{
		ApprovedPath: strings.TrimSpace(g.ApprovedPath),
		ResolvedPath: strings.TrimSpace(g.ResolvedPath),
	}
}

func SocketGrantPairSet(grants []confine.SocketGrant) map[string]struct{} {
	out := make(map[string]struct{}, len(grants))
	for _, g := range grants {
		out[SocketGrantPairKey(g)] = struct{}{}
	}
	return out
}

func GrantAuthorized(g confine.SocketGrant, authorized []confine.SocketGrant) bool {
	key := SocketGrantPairKey(g)
	for _, a := range authorized {
		if SocketGrantPairKey(a) == key {
			return true
		}
	}
	return false
}

func SocketGrantDigests(grants []confine.SocketGrant) []string {
	out := make([]string, 0, len(grants))
	for _, g := range grants {
		if d := confine.SocketPathsDigest([]confine.SocketGrant{g}); d != "" {
			out = append(out, d)
		}
	}
	return out
}
