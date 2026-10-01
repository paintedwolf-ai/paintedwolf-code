package approvalstate

import (
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
)

// pathRunGrantCap bounds the unnamed path grants one chat derives while commands run.
const pathRunGrantCap = 32

// SandboxPathGrantRuntime stores one independent read or write grant set.
type SandboxPathGrantRuntime struct {
	guard sandboxAskGuard[PathChatGrant]
}

// PathChatGrant is one revocable chat path authority.
type PathChatGrant struct {
	ID        string
	Root      string
	CreatedAt time.Time
	// ExpiresAt bounds the grant; nil means the chat's lifetime.
	ExpiresAt          *time.Time
	SourceCheckpointID string
}

func (g PathChatGrant) grantID() string           { return g.ID }
func (g PathChatGrant) grantCheckpointID() string { return g.SourceCheckpointID }

// expired reports whether the grant's time bound has passed.
func (g PathChatGrant) expired(now time.Time) bool {
	return g.ExpiresAt != nil && !g.ExpiresAt.After(now)
}

// Live reports whether the grant still carries authority.
func (g PathChatGrant) Live(now time.Time) bool { return !g.expired(now) }

// NewSandboxPathGrantRuntime constructs empty guard state.
func NewSandboxPathGrantRuntime() *SandboxPathGrantRuntime {
	return &SandboxPathGrantRuntime{guard: sandboxAskGuard[PathChatGrant]{
		overlayCap:   pathRunGrantCap,
		normalizeKey: confine.NormalizeWriteRootKey,
	}}
}

// Begin reserves an invocation before checkpoint creation.
func (r *SandboxPathGrantRuntime) Begin(rootSessionID, proposedRoot, toolCallID string) (SandboxAskBegin, string) {
	if r == nil {
		return SandboxAskMint, ""
	}
	return r.guard.begin(rootSessionID, proposedRoot, toolCallID)
}

// RegisterPending records checkpointID for proposedRoot after a successful mint.
func (r *SandboxPathGrantRuntime) RegisterPending(rootSessionID, proposedRoot, checkpointID string) {
	if r == nil {
		return
	}
	r.guard.registerPending(rootSessionID, proposedRoot, checkpointID)
}

// AbortMint clears a tool_call reservation when RequestCheckpoint fails after Begin=Mint.
func (r *SandboxPathGrantRuntime) AbortMint(rootSessionID, toolCallID string) {
	if r == nil {
		return
	}
	r.guard.abortMint(rootSessionID, toolCallID)
}

// Finish clears pending for the root after HITL resolves. When denied, inserts deny-set.
func (r *SandboxPathGrantRuntime) Finish(rootSessionID, proposedRoot string, denied bool) {
	if r == nil {
		return
	}
	r.guard.finish(rootSessionID, proposedRoot, denied)
}

// RecordDenied records a combined review without clearing another open card.
func (r *SandboxPathGrantRuntime) RecordDenied(rootSessionID, proposedRoot string) {
	if r != nil {
		r.guard.recordDenied(rootSessionID, proposedRoot)
	}
}

// ClearDenied removes a root from the deny-set after approval.
func (r *SandboxPathGrantRuntime) ClearDenied(rootSessionID, proposedRoot string) {
	if r == nil {
		return
	}
	r.guard.clearDenied(rootSessionID, proposedRoot)
}

// NoteUserIntentBoundary clears the chat's deny-set on a new user turn.
func (r *SandboxPathGrantRuntime) NoteUserIntentBoundary(chatSessionID string) {
	if r == nil {
		return
	}
	r.guard.noteUserIntentBoundary(chatSessionID)
}

// GrantSessionWriteRoot records implicit chat authority.
func (r *SandboxPathGrantRuntime) GrantSessionWriteRoot(rootSessionID, root string) {
	_ = r.grantChat(rootSessionID, root, "", "", nil)
}

// GrantChat records a human-approved, revocable chat path lease. A nil
// expiresAt lasts for the chat; a deadline is the rung the person chose.
func (r *SandboxPathGrantRuntime) GrantChat(rootSessionID, root, grantID, checkpointID string, expiresAt *time.Time) bool {
	return r.grantChat(rootSessionID, root, strings.TrimSpace(grantID), strings.TrimSpace(checkpointID), expiresAt)
}

// Reinstalling the same live path grant is a no-op; an expired one is replaced.
func (r *SandboxPathGrantRuntime) grantChat(rootSessionID, root, grantID, checkpointID string, expiresAt *time.Time) bool {
	if r == nil {
		return false
	}
	key := confine.NormalizeWriteRootKey(root)
	if key == "" || key == "." {
		return false
	}
	now := time.Now().UTC()
	return r.guard.mergeGrants(rootSessionID, func(existing []PathChatGrant) ([]PathChatGrant, bool) {
		next := make([]PathChatGrant, 0, len(existing)+1)
		for _, grant := range existing {
			if grant.ID == grantID && grant.Root == key {
				if !grant.expired(now) {
					return nil, false
				}
				continue
			}
			next = append(next, grant)
		}
		return append(next, PathChatGrant{
			ID: grantID, Root: key, CreatedAt: now, ExpiresAt: expiresAt, SourceCheckpointID: checkpointID,
		}), true
	})
}

// ListChatGrants returns the chat write-root leases for one session tree.
func (r *SandboxPathGrantRuntime) ListChatGrants(rootSessionID string) []PathChatGrant {
	if r == nil {
		return nil
	}
	return r.guard.listGrants(rootSessionID)
}

// ListAllChatGrants returns human-approved write-root leases for every chat.
func (r *SandboxPathGrantRuntime) ListAllChatGrants() map[string][]PathChatGrant {
	if r == nil {
		return nil
	}
	return r.guard.listAllGrants()
}

// FindByID locates a human-approved chat write-root lease.
func (r *SandboxPathGrantRuntime) FindByID(id string) (PathChatGrant, string, bool) {
	if r == nil {
		return PathChatGrant{}, "", false
	}
	return r.guard.findByID(id)
}

// RevokeByID removes every write root installed by one approval option.
func (r *SandboxPathGrantRuntime) RevokeByID(id string) (PathChatGrant, bool) {
	if r == nil {
		return PathChatGrant{}, false
	}
	return r.guard.revokeByID(id, "")
}

// RevokeByIDInstalledBy removes write roots only when their installing checkpoint still identifies them.
func (r *SandboxPathGrantRuntime) RevokeByIDInstalledBy(id, checkpointID string) (PathChatGrant, bool) {
	checkpointID = strings.TrimSpace(checkpointID)
	if r == nil || checkpointID == "" {
		return PathChatGrant{}, false
	}
	return r.guard.revokeByID(id, checkpointID)
}

// SessionWriteRoots is the chat's live authority: approved paths not yet
// created are included, grants past their deadline are not.
func (r *SandboxPathGrantRuntime) SessionWriteRoots(rootSessionID string) []string {
	if r == nil {
		return nil
	}
	var roots []string
	now := time.Now()
	r.guard.readGrants(rootSessionID, func(grants []PathChatGrant) {
		if len(grants) == 0 {
			return
		}
		seen := map[string]struct{}{}
		roots = make([]string, 0, len(grants))
		for _, grant := range grants {
			if grant.expired(now) {
				continue
			}
			if _, exists := seen[grant.Root]; exists {
				continue
			}
			seen[grant.Root] = struct{}{}
			roots = append(roots, grant.Root)
		}
	})
	return roots
}

// ReleaseRun clears one run's reviews and run grants; approved grants last for the chat.
func (r *SandboxPathGrantRuntime) ReleaseRun(rootSessionID string) {
	if r != nil {
		r.guard.releaseRun(rootSessionID)
	}
}

// ForgetSession releases every grant when the chat is disposed.
func (r *SandboxPathGrantRuntime) ForgetSession(rootSessionID string) {
	if r == nil {
		return
	}
	r.guard.forgetSession(rootSessionID)
}
