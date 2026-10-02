package settings

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/presence"
)

// DeferredGate requires approval until Seal attaches the policy producers.
type DeferredGate struct {
	mu     sync.RWMutex
	sealed hitl.ApprovalGate
}

func NewDeferredGate() *DeferredGate { return &DeferredGate{} }

func (d *DeferredGate) seal(g hitl.ApprovalGate) {
	d.mu.Lock()
	d.sealed = g
	d.mu.Unlock()
}

func (d *DeferredGate) Sealed() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.sealed != nil
}

func (d *DeferredGate) inner() hitl.ApprovalGate {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.sealed
}

// Evaluate delegates once sealed, and fails closed before that.
func (d *DeferredGate) Evaluate(ctx context.Context, action hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	inner := d.inner()
	if inner == nil {
		return &hitl.ApprovalResult{Decision: gate.UnsealedDecision()}, nil
	}
	return inner.Evaluate(ctx, action)
}

// An unsealed gate has no policy facts to support grant offers.
func (d *DeferredGate) GrantOffers(action hitl.ProposedAction, result *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	inner := d.inner()
	if inner == nil {
		return nil
	}
	return inner.GrantOffers(action, result)
}

func (d *DeferredGate) AbsorbedGrantOffers(action hitl.ProposedAction, result *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	inner := d.inner()
	if inner == nil {
		return nil
	}
	return inner.AbsorbedGrantOffers(action, result)
}

// An unsealed gate cannot record grants.
func (d *DeferredGate) ApplyGrant(grant hitl.ApprovalGrant) (bool, error) {
	inner := d.inner()
	if inner == nil {
		return false, nil
	}
	return inner.ApplyGrant(grant)
}

// GrantCovers is false until the inner gate is sealed.
func (d *DeferredGate) GrantCovers(action hitl.ProposedAction) bool {
	inner := d.inner()
	if inner == nil {
		return false
	}
	return inner.GrantCovers(action)
}

func (d *DeferredGate) HostResourceLeaseCovers(action hitl.ProposedAction) bool {
	inner := d.inner()
	if inner == nil {
		return false
	}
	return inner.HostResourceLeaseCovers(action)
}

// RevokeGrant is a no-op until the inner gate is sealed.
func (d *DeferredGate) RevokeGrant(id string) (bool, error) {
	inner := d.inner()
	if inner == nil {
		return false, nil
	}
	return inner.RevokeGrant(id)
}

func (d *DeferredGate) RevokeGrantInstalledBy(id, operationID string) (bool, error) {
	inner := d.inner()
	if inner == nil {
		return false, nil
	}
	return inner.RevokeGrantInstalledBy(id, operationID)
}

// ListGrants returns nothing while unsealed.
func (d *DeferredGate) ListGrants(chatSessionID string) []hitl.ApprovalGrant {
	inner := d.inner()
	if inner == nil {
		return nil
	}
	return inner.ListGrants(chatSessionID)
}

// An unsealed gate cannot confirm secret authority.
func (d *DeferredGate) SecretReleaseCovered(chatSessionID, projectID, destinationID, surface string, fingerprints, held []string) (bool, map[string]string) {
	inner := d.inner()
	if inner == nil {
		return false, nil
	}
	return inner.SecretReleaseCovered(chatSessionID, projectID, destinationID, surface, fingerprints, held)
}

// An unsealed gate cannot confirm standing redaction authority.
func (d *DeferredGate) SecretRedactionStanding(projectID string, fingerprints []string) bool {
	inner := d.inner()
	return inner != nil && inner.SecretRedactionStanding(projectID, fingerprints)
}

func (d *DeferredGate) PutAskQuiet(q hitl.AskQuiet, ttlSeconds int) (hitl.AskQuiet, bool) {
	inner := d.inner()
	if inner == nil {
		return hitl.AskQuiet{}, false
	}
	return inner.PutAskQuiet(q, ttlSeconds)
}

func (d *DeferredGate) AskQuietLive(chatSessionID, key string) (hitl.AskQuiet, bool) {
	inner := d.inner()
	if inner == nil {
		return hitl.AskQuiet{}, false
	}
	return inner.AskQuietLive(chatSessionID, key)
}

func (d *DeferredGate) NoteAskQuietSuppressed(chatSessionID, key string) {
	if inner := d.inner(); inner != nil {
		inner.NoteAskQuietSuppressed(chatSessionID, key)
	}
}

func (d *DeferredGate) ListAskQuiets(chatSessionID string) []hitl.AskQuiet {
	inner := d.inner()
	if inner == nil {
		return nil
	}
	return inner.ListAskQuiets(chatSessionID)
}

func (d *DeferredGate) RevokeAskQuiet(id string) bool {
	inner := d.inner()
	return inner != nil && inner.RevokeAskQuiet(id)
}

func (d *DeferredGate) RevokeAskQuietInstalledBy(id, operationID string) bool {
	inner := d.inner()
	return inner != nil && inner.RevokeAskQuietInstalledBy(id, operationID)
}

// ForgetSession is a no-op while unsealed.
func (d *DeferredGate) ForgetSession(chatSessionID string) {
	if inner := d.inner(); inner != nil {
		inner.ForgetSession(chatSessionID)
	}
}

// GateBuilder collects startup producers before constructing the gate once.
type GateBuilder struct {
	mu      sync.Mutex
	store   *ApprovalStore
	sources Sources
	handle  *DeferredGate
}

// An unwired detection source reports missing facts.
func NewGateBuilder(store *ApprovalStore) (*GateBuilder, *DeferredGate) {
	handle := NewDeferredGate()
	return &GateBuilder{store: store, handle: handle}, handle
}

func (b *GateBuilder) WithDetections(src func() DetectionSource) *GateBuilder {
	return b.with(func(s *Sources) {
		if src != nil {
			s.Detections = src
		}
	})
}

func (b *GateBuilder) WithPins(src MCPToolPinSource) *GateBuilder {
	return b.with(func(s *Sources) {
		if src != nil {
			s.Pins = src
		}
	})
}

func (b *GateBuilder) WithApprovalRules(src ApprovalRuleCatalogSource) *GateBuilder {
	return b.with(func(s *Sources) {
		if src != nil {
			s.ApprovalRules = src
		}
	})
}

// WithReleaseLedger lets attested release grants cover person-held values.
func (b *GateBuilder) WithReleaseLedger(ledger *presence.ReleaseLedger) *GateBuilder {
	return b.with(func(s *Sources) { s.ReleaseLedger = ledger })
}

func (b *GateBuilder) with(apply func(*Sources)) *GateBuilder {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.handle.Sealed() {
		panic("settings: approval gate wiring is sealed")
	}
	apply(&b.sources)
	return b
}

// Seal retains the same gate and its session grants on repeated calls.
func (b *GateBuilder) Seal() hitl.ApprovalGate {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if built := b.handle.inner(); built != nil {
		return built
	}
	built := NewRuleApprovalGate(b.store, b.sources)
	b.handle.seal(built)
	return built
}
