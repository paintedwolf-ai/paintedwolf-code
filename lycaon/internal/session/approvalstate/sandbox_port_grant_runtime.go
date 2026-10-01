package approvalstate

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// portRunGrantCap bounds the unnamed port grants one chat derives while commands run.
const portRunGrantCap = 16

// SandboxPortGrantRuntime stores one independent listener or connection grant set.
type SandboxPortGrantRuntime struct {
	guard sandboxAskGuard[PortLeaseChatGrant]
}

// PortLeaseChatGrant is a revocable lease held in its capability’s runtime.
type PortLeaseChatGrant struct {
	ID                 string
	Ports              []uint16
	CreatedAt          time.Time
	ExpiresAt          *time.Time
	SourceCheckpointID string
}

func (g PortLeaseChatGrant) grantID() string           { return g.ID }
func (g PortLeaseChatGrant) grantCheckpointID() string { return g.SourceCheckpointID }

func (g PortLeaseChatGrant) expired(now time.Time) bool {
	return g.ExpiresAt != nil && !g.ExpiresAt.After(now)
}

// PortGrantKey canonicalizes a port set for guard and coalesce state:
// "any" for an unnarrowed grant, else the sorted port list.
func PortGrantKey(ports []uint16) string {
	if len(ports) == 0 {
		return "any"
	}
	cp := append([]uint16(nil), ports...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	parts := make([]string, 0, len(cp))
	for _, p := range cp {
		parts = append(parts, strconv.Itoa(int(p)))
	}
	return strings.Join(parts, ",")
}

// NewSandboxPortGrantRuntime constructs empty guard state.
func NewSandboxPortGrantRuntime() *SandboxPortGrantRuntime {
	return &SandboxPortGrantRuntime{guard: sandboxAskGuard[PortLeaseChatGrant]{
		overlayCap: portRunGrantCap,
	}}
}

// Begin reserves an invocation before checkpoint creation.
func (r *SandboxPortGrantRuntime) Begin(rootSessionID, portKey, toolCallID string) (SandboxAskBegin, string) {
	if r == nil {
		return SandboxAskMint, ""
	}
	return r.guard.begin(rootSessionID, portKey, toolCallID)
}

// RegisterPending publishes the checkpoint for a reserved capability.
func (r *SandboxPortGrantRuntime) RegisterPending(rootSessionID, portKey, checkpointID string) {
	if r == nil {
		return
	}
	r.guard.registerPending(rootSessionID, portKey, checkpointID)
}

// AbortMint clears a tool_call reservation when RequestCheckpoint fails.
func (r *SandboxPortGrantRuntime) AbortMint(rootSessionID, toolCallID string) {
	if r == nil {
		return
	}
	r.guard.abortMint(rootSessionID, toolCallID)
}

// Finish clears pending after HITL resolves. When denied, inserts deny-set.
func (r *SandboxPortGrantRuntime) Finish(rootSessionID, portKey string, denied bool) {
	if r == nil {
		return
	}
	r.guard.finish(rootSessionID, portKey, denied)
}

// RecordDenied preserves concurrent pending reviews.
func (r *SandboxPortGrantRuntime) RecordDenied(rootSessionID, portKey string) {
	if r != nil {
		r.guard.recordDenied(rootSessionID, portKey)
	}
}

// ClearDenied permits another review of the capability.
func (r *SandboxPortGrantRuntime) ClearDenied(rootSessionID, portKey string) {
	if r == nil {
		return
	}
	r.guard.clearDenied(rootSessionID, portKey)
}

// NoteUserIntentBoundary clears the chat's deny-set on a new user turn.
func (r *SandboxPortGrantRuntime) NoteUserIntentBoundary(chatSessionID string) {
	if r == nil {
		return
	}
	r.guard.noteUserIntentBoundary(chatSessionID)
}

// GrantSessionPorts records implicit chat authority.
func (r *SandboxPortGrantRuntime) GrantSessionPorts(rootSessionID string, ports []uint16) {
	_ = r.grantChat(rootSessionID, ports, "", "", nil)
}

// GrantChat records a human-approved, revocable chat port lease.
func (r *SandboxPortGrantRuntime) GrantChat(rootSessionID string, ports []uint16, grantID, checkpointID string, expiresAt *time.Time) bool {
	return r.grantChat(rootSessionID, ports, strings.TrimSpace(grantID), strings.TrimSpace(checkpointID), expiresAt)
}

// Matching live grants are unchanged; expired grants are replaced.
func (r *SandboxPortGrantRuntime) grantChat(rootSessionID string, ports []uint16, grantID, checkpointID string, expiresAt *time.Time) bool {
	if r == nil {
		return false
	}
	key := PortGrantKey(ports)
	now := time.Now().UTC()
	return r.guard.mergeGrants(rootSessionID, func(existing []PortLeaseChatGrant) ([]PortLeaseChatGrant, bool) {
		next := make([]PortLeaseChatGrant, 0, len(existing)+1)
		for _, grant := range existing {
			if grant.ID != grantID {
				next = append(next, grant)
				continue
			}
			if PortGrantKey(grant.Ports) != key {
				return nil, false
			}
			if !grant.expired(now) {
				return nil, false
			}
		}
		return append(next, PortLeaseChatGrant{
			ID: grantID, Ports: append([]uint16(nil), ports...),
			CreatedAt: now, ExpiresAt: expiresAt, SourceCheckpointID: checkpointID,
		}), true
	})
}

// SessionPorts returns live authority; granted with empty ports covers every port.
func (r *SandboxPortGrantRuntime) SessionPorts(rootSessionID string) (bool, []uint16) {
	if r == nil {
		return false, nil
	}
	granted := false
	var ports []uint16
	r.guard.readGrants(rootSessionID, func(grants []PortLeaseChatGrant) {
		if len(grants) == 0 {
			return
		}
		now := time.Now().UTC()
		seen := map[uint16]struct{}{}
		collected := []uint16{}
		live := 0
		for _, grant := range grants {
			if grant.expired(now) {
				continue
			}
			live++
			if len(grant.Ports) == 0 {
				// One unnarrowed lease covers everything.
				granted, ports = true, nil
				return
			}
			for _, p := range grant.Ports {
				if _, dup := seen[p]; dup {
					continue
				}
				seen[p] = struct{}{}
				collected = append(collected, p)
			}
		}
		if live == 0 {
			return
		}
		sort.Slice(collected, func(i, j int) bool { return collected[i] < collected[j] })
		granted, ports = true, collected
	})
	return granted, ports
}

// ListChatGrants returns the human-approved port leases for one session tree.
func (r *SandboxPortGrantRuntime) ListChatGrants(rootSessionID string) []PortLeaseChatGrant {
	if r == nil {
		return nil
	}
	return r.guard.listGrants(rootSessionID)
}

// ListAllChatGrants returns human-approved port leases for every chat.
func (r *SandboxPortGrantRuntime) ListAllChatGrants() map[string][]PortLeaseChatGrant {
	if r == nil {
		return nil
	}
	return r.guard.listAllGrants()
}

// FindByID locates a human-approved chat port lease.
func (r *SandboxPortGrantRuntime) FindByID(id string) (PortLeaseChatGrant, string, bool) {
	if r == nil {
		return PortLeaseChatGrant{}, "", false
	}
	return r.guard.findByID(id)
}

// RevokeByID removes the port lease installed by one approval option.
func (r *SandboxPortGrantRuntime) RevokeByID(id string) (PortLeaseChatGrant, bool) {
	if r == nil {
		return PortLeaseChatGrant{}, false
	}
	return r.guard.revokeByID(id, "")
}

// RevokeByIDInstalledBy checks the installing checkpoint before revocation.
func (r *SandboxPortGrantRuntime) RevokeByIDInstalledBy(id, checkpointID string) (PortLeaseChatGrant, bool) {
	checkpointID = strings.TrimSpace(checkpointID)
	if r == nil || checkpointID == "" {
		return PortLeaseChatGrant{}, false
	}
	return r.guard.revokeByID(id, checkpointID)
}

// ReleaseRun clears one run's reviews and run grants; approved grants last for the chat.
func (r *SandboxPortGrantRuntime) ReleaseRun(rootSessionID string) {
	if r != nil {
		r.guard.releaseRun(rootSessionID)
	}
}

// ForgetSession releases every grant when the chat is disposed.
func (r *SandboxPortGrantRuntime) ForgetSession(rootSessionID string) {
	if r == nil {
		return
	}
	r.guard.forgetSession(rootSessionID)
}
