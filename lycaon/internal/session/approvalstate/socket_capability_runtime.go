package approvalstate

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/socketref"
)

// SocketCapabilityRuntime keeps session-tree grants and per-call permits in memory.
// The shared ask guard owns grant listing and revocation.
type SocketCapabilityRuntime struct {
	guard sandboxAskGuard[SocketChatGrant]
	// mu serializes resource ownership; the guard also protects logical rows.
	mu      sync.Mutex
	permits scopedstore.LRU[*socketPermitSlot]
	expiry  *time.Timer
	closed  bool
}

// SocketChatGrant is a chat-scoped AF_UNIX grant recorded after human approval.
type SocketChatGrant struct {
	ID           string
	ApprovedPath string
	ResolvedPath string
	Binding      socketref.Binding
	Unavailable  bool
	resource     *socketref.Lease
	CreatedAt    time.Time
	// ExpiresAt bounds the grant; nil means the chat's lifetime.
	ExpiresAt          *time.Time
	SourceCheckpointID string
	SourceActionDigest string
}

// expired reports whether the grant's time bound has passed.
func (tg SocketChatGrant) expired(now time.Time) bool {
	return tg.ExpiresAt != nil && !tg.ExpiresAt.After(now)
}

func (tg SocketChatGrant) grantID() string           { return tg.ID }
func (tg SocketChatGrant) grantCheckpointID() string { return tg.SourceCheckpointID }

var (
	errSocketPermitMismatch = errors.New("session: socket permit mismatch")
)

// NewSocketCapabilityRuntime constructs empty socket capability state.
func NewSocketCapabilityRuntime() *SocketCapabilityRuntime {
	return &SocketCapabilityRuntime{
		guard: sandboxAskGuard[SocketChatGrant]{overlayCap: confine.MaxSocketGrants},
	}
}

// AppliedGrants revalidates the chat's live socket grants for one execution boundary.
func (r *SocketCapabilityRuntime) AppliedGrants(rootSessionID string) []confine.SocketGrant {
	if r == nil {
		return nil
	}
	var out []confine.SocketGrant
	stale := make(map[string]string)
	now := time.Now().UTC()
	r.guard.readGrants(rootSessionID, func(grants []SocketChatGrant) {
		seen := map[string]struct{}{}
		out = make([]confine.SocketGrant, 0, len(grants))
		for _, tg := range grants {
			if tg.expired(now) || tg.Unavailable {
				continue
			}
			g := confine.SocketGrant{ApprovedPath: tg.ApprovedPath, ResolvedPath: tg.ResolvedPath, Binding: tg.Binding}
			if err := confine.RevalidateSocketGrant(g); err != nil {
				if tg.Binding.Mode() == socketref.RetainedObject {
					stale[tg.ID] = tg.Binding.Key()
				}
				continue
			}
			key := socketGrantPairKey(g)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, g)
		}
	})
	r.retireSocketOwners(rootSessionID, stale)
	if len(out) == 0 {
		return nil
	}
	if len(out) > confine.MaxSocketGrants {
		out = out[len(out)-confine.MaxSocketGrants:]
	}
	return out
}

// ListChatGrants includes stale rows for review.
func (r *SocketCapabilityRuntime) ListChatGrants(rootSessionID string) []SocketChatGrant {
	if r == nil {
		return nil
	}
	out := r.guard.listGrants(rootSessionID)
	if len(out) == 0 {
		return nil
	}
	return cloneSocketGrants(out)
}

// ListAllChatGrants groups chat grants by root session.
func (r *SocketCapabilityRuntime) ListAllChatGrants() map[string][]SocketChatGrant {
	if r == nil {
		return nil
	}
	out := r.guard.listAllGrants()
	for root, grants := range out {
		out[root] = cloneSocketGrants(grants)
	}
	return out
}

// GrantChat records a chat-scoped socket grant without replacing live authority.
func (r *SocketCapabilityRuntime) GrantChat(rootSessionID string, g confine.SocketGrant, grantID, checkpointID, actionDigest string, expiresAt *time.Time) bool {
	if r == nil {
		return false
	}
	g = normalizeSocketGrant(g)
	grantID = strings.TrimSpace(grantID)
	if g.ApprovedPath == "" || g.ResolvedPath == "" || grantID == "" {
		return false
	}
	if confine.RevalidateSocketGrant(g) != nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	var resource *socketref.Lease
	if g.Binding.Mode() == socketref.RetainedObject {
		var err error
		resource, err = g.Binding.Acquire()
		if err != nil {
			return false
		}
	}
	var removed []*socketref.Lease
	key := socketGrantPairKey(g)
	now := time.Now().UTC()
	created := r.guard.mergeGrants(rootSessionID, func(existing []SocketChatGrant) ([]SocketChatGrant, bool) {
		next := make([]SocketChatGrant, 0, len(existing)+1)
		for _, prior := range existing {
			samePair := socketGrantPairKey(confine.SocketGrant{
				ApprovedPath: prior.ApprovedPath, ResolvedPath: prior.ResolvedPath, Binding: prior.Binding,
			}) == key
			if prior.ID != grantID || !samePair {
				if prior.ApprovedPath == g.ApprovedPath && prior.Binding.Mode() == socketref.RetainedObject && !samePair {
					removed = append(removed, prior.resource)
					prior.resource = nil
					prior.Unavailable = true
				}
				next = append(next, prior)
				continue
			}
			// A live grant for the same id and pair already covers this.
			if !prior.expired(now) && !prior.Unavailable {
				return nil, false
			}
			removed = append(removed, prior.resource)
		}
		return append(next, SocketChatGrant{
			ID:           grantID,
			ApprovedPath: g.ApprovedPath,
			ResolvedPath: g.ResolvedPath,
			Binding:      g.Binding, resource: resource,
			CreatedAt:          now,
			ExpiresAt:          cloneSocketExpiry(expiresAt),
			SourceCheckpointID: strings.TrimSpace(checkpointID),
			SourceActionDigest: strings.TrimSpace(actionDigest),
		}), true
	})
	if !created {
		resource.Close()
		return false
	}
	for _, prior := range removed {
		prior.Close()
	}
	r.scheduleSocketExpiryLocked()
	return true
}

// RevokeByID removes the whole chat grant set sharing an opaque approval option
// id. A multi-socket approval is one atomic authority unit.
func (r *SocketCapabilityRuntime) RevokeByID(id string) (SocketChatGrant, bool) {
	return r.revokeByIDInstalledBy(id, "")
}

// RevokeByIDInstalledBy removes a chat grant only when its installing checkpoint still identifies it.
func (r *SocketCapabilityRuntime) RevokeByIDInstalledBy(id, checkpointID string) (SocketChatGrant, bool) {
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return SocketChatGrant{}, false
	}
	return r.revokeByIDInstalledBy(id, checkpointID)
}

func (r *SocketCapabilityRuntime) revokeByIDInstalledBy(id, checkpointID string) (SocketChatGrant, bool) {
	if r == nil {
		return SocketChatGrant{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	before := r.guard.listAllGrants()
	removed, ok := r.guard.revokeByID(id, checkpointID)
	if ok {
		for _, grants := range before {
			for _, grant := range grants {
				if grant.ID == id && (checkpointID == "" || grant.SourceCheckpointID == checkpointID) {
					grant.resource.Close()
				}
			}
		}
		r.scheduleSocketExpiryLocked()
	}
	return removed, ok
}

// FindByID locates a chat grant by opaque id.
func (r *SocketCapabilityRuntime) FindByID(id string) (SocketChatGrant, string, bool) {
	if r == nil {
		return SocketChatGrant{}, "", false
	}
	grant, root, ok := r.guard.findByID(id)
	grant.ExpiresAt = cloneSocketExpiry(grant.ExpiresAt)
	grant.resource = nil
	return grant, root, ok
}

// IssuePermit mints an ephemeral current-call permit.
func (r *SocketCapabilityRuntime) IssuePermit(rootSessionID string, identity hitl.EffectPermitIdentity, actionDigest string, g confine.SocketGrant) {
	if r == nil {
		return
	}
	g = normalizeSocketGrant(g)
	if g.ApprovedPath == "" || g.ResolvedPath == "" {
		return
	}
	actionDigest = strings.TrimSpace(actionDigest)
	if !identity.Valid() || actionDigest == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	var resource *socketref.Lease
	if g.Binding.Mode() == socketref.RetainedObject {
		var err error
		resource, err = g.Binding.Acquire()
		if err != nil {
			return
		}
	}
	slotKey := socketPermitKey(identity, actionDigest, g)
	for _, prior := range r.permits.Store(slotKey, &socketPermitSlot{grant: g, resource: resource, rootSessionID: strings.TrimSpace(rootSessionID), sessionID: identity.SessionID}) {
		prior.resource.Close()
	}
}

// RevokePermit removes an unconsumed permit. It is the rollback path for an
// approval resolution that could not commit after authority installation.
func (r *SocketCapabilityRuntime) RevokePermit(identity hitl.EffectPermitIdentity, actionDigest string, g confine.SocketGrant) {
	if r == nil || !identity.Valid() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if slot, ok := r.permits.LoadAndDelete(socketPermitKey(identity, strings.TrimSpace(actionDigest), normalizeSocketGrant(g))); ok {
		slot.resource.Close()
	}
}

// ConsumePermit atomically consumes a current-call permit once for the invocation.
func (r *SocketCapabilityRuntime) ConsumePermit(identity hitl.EffectPermitIdentity, actionDigest string, g confine.SocketGrant) (bool, error) {
	if r == nil {
		return false, errSocketPermitMismatch
	}
	g = normalizeSocketGrant(g)
	actionDigest = strings.TrimSpace(actionDigest)
	if !identity.Valid() || actionDigest == "" || g.ApprovedPath == "" || g.ResolvedPath == "" {
		return false, errSocketPermitMismatch
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	slotKey := socketPermitKey(identity, actionDigest, g)
	slot, ok := r.permits.LoadAndDelete(slotKey)
	if !ok {
		return false, nil
	}
	defer slot.resource.Close()
	if socketGrantPairKey(slot.grant) != socketGrantPairKey(g) {
		return false, errSocketPermitMismatch
	}
	return true, nil
}

// AuthorizedGrants returns the subset of requested grants covered by chat overlay
// or an unconsumed current-call permit for this invocation.
func (r *SocketCapabilityRuntime) AuthorizedGrants(rootSessionID string, identity hitl.EffectPermitIdentity, actionDigest string, requested []confine.SocketGrant) []confine.SocketGrant {
	if r == nil || !identity.Valid() || len(requested) == 0 {
		return nil
	}
	chatByPair := map[string]confine.SocketGrant{}
	for _, g := range r.AppliedGrants(rootSessionID) {
		chatByPair[socketGrantPairKey(g)] = g
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]confine.SocketGrant, 0, len(requested))
	for _, req := range requested {
		req = normalizeSocketGrant(req)
		key := socketGrantPairKey(req)
		if tg, ok := chatByPair[key]; ok {
			out = append(out, tg)
			continue
		}
		slotKey := socketPermitKey(identity, strings.TrimSpace(actionDigest), req)
		if slot, ok := r.permits.Load(slotKey); ok && socketGrantPairKey(slot.grant) == key {
			out = append(out, req)
		}
	}
	return out
}

func (r *SocketCapabilityRuntime) forgetPermitsLocked(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	for key, slot := range r.permits.Snapshot() {
		if slot.sessionID == sessionID || slot.rootSessionID == sessionID {
			r.permits.Delete(key)
			slot.resource.Close()
		}
	}
}

// ReleaseRun clears per-call permits and open reviews; approved grants last for the chat.
func (r *SocketCapabilityRuntime) ReleaseRun(rootSessionID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.guard.releaseRun(rootSessionID)
	r.forgetPermitsLocked(rootSessionID)
}

// ForgetSession clears grants before per-call permits to narrow concurrent reads first.
func (r *SocketCapabilityRuntime) ForgetSession(rootSessionID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	prior := r.guard.listGrants(rootSessionID)
	r.guard.forgetSession(rootSessionID)
	for _, grant := range prior {
		grant.resource.Close()
	}
	r.scheduleSocketExpiryLocked()
	r.forgetPermitsLocked(rootSessionID)
}

type socketPermitSlot struct {
	grant         confine.SocketGrant
	resource      *socketref.Lease
	rootSessionID string
	sessionID     string
}

func normalizeSocketGrant(g confine.SocketGrant) confine.SocketGrant {
	return confine.SocketGrant{
		ApprovedPath: strings.TrimSpace(g.ApprovedPath),
		ResolvedPath: strings.TrimSpace(g.ResolvedPath), Binding: g.Binding,
	}
}

func socketGrantPairKey(g confine.SocketGrant) string {
	g = normalizeSocketGrant(g)
	return g.ApprovedPath + "\x00" + g.ResolvedPath + "\x00" + g.Binding.Key()
}

func socketPermitKey(identity hitl.EffectPermitIdentity, actionDigest string, g confine.SocketGrant) string {
	return identity.Key() + "\x01" + actionDigest + "\x01" + socketGrantPairKey(g)
}
