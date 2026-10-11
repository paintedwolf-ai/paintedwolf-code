package closeouts

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/pkg/api"
)

// Service keeps prompt budgets separate from shared root-cycle state.
// Its lock makes updates, resets, and eviction atomic.
type Store interface {
	Get(context.Context, string) (*api.Session, error)
}
type Service struct {
	store   Store
	mu      sync.Mutex
	prompts scopedstore.LRU[closeoutPromptState]
	cycles  scopedstore.LRU[closeoutCycleState]
}

type closeoutPromptState struct {
	rootID           string
	delays           [DelayKinds]int
	groundingRejects int
}

type closeoutCycleState struct {
	groundingRejects int
	stall            closeoutStall
}

type DelayKind int

const (
	ProgressDelay DelayKind = iota
	VerdictDelay
	GateDelay
	DelayKinds
)

func (c *Service) beginPrompt(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prompts.Delete(sessionID)
}

func (c *Service) beginIntent(rootID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prompts.Delete(rootID)
	c.cycles.Delete(rootID)
}

// beginPhase restarts the root cycle's grounding friction when a workflow
// enters a new phase, so rejects spent in one phase do not exhaust the next.
// Same-phase re-entry continues the cycle.
func (c *Service) beginPhase(rootID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cycle, ok := c.cycles.Load(rootID)
	if !ok {
		return
	}
	cycle.groundingRejects = 0
	c.cycles.Store(rootID, cycle)
}

// forget preserves a surviving parent's cycle when disposing a worker.
func (c *Service) Forget(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forgetLocked(sessionID)
}

func (c *Service) forgetLocked(sessionID string) {
	c.prompts.Delete(sessionID)
	c.cycles.Delete(sessionID)
	for id, prompt := range c.prompts.Snapshot() {
		if prompt.rootID == sessionID {
			c.prompts.Delete(id)
		}
	}
}

func (c *Service) Rewind(sessionID, rootID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forgetLocked(sessionID)
	if rootID != sessionID {
		c.forgetLocked(rootID)
	}
}

// delay returns the pre-attempt count and charges only the named obligation.
func (c *Service) Delay(sessionID, rootID string, kind DelayKind, eligible bool, limit int) (count int, delayed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prompt, _ := c.prompts.Load(sessionID)
	count = prompt.delays[kind]
	if !eligible || count >= limit {
		return count, false
	}
	prompt.rootID = rootID
	prompt.delays[kind]++
	c.recordFrictionLocked(sessionID, rootID, prompt)
	return count, true
}

func (c *Service) recordFriction(sessionID, rootID string) guidance.GroundingFriction {
	c.mu.Lock()
	defer c.mu.Unlock()
	prompt, _ := c.prompts.Load(sessionID)
	return c.recordFrictionLocked(sessionID, rootID, prompt)
}

func (c *Service) recordFrictionLocked(sessionID, rootID string, prompt closeoutPromptState) guidance.GroundingFriction {
	prompt.rootID = rootID
	prompt.groundingRejects++
	cycle, _ := c.cycles.Load(rootID)
	cycle.groundingRejects++
	c.prompts.Store(sessionID, prompt)
	c.cycles.Store(rootID, cycle)
	return guidance.GroundingFriction{Remaining: min(
		limits.DefaultGroundingRejectsPerTurn-prompt.groundingRejects,
		limits.DefaultGroundingRejectsPerCycle-cycle.groundingRejects,
	)}
}

func (m *Service) BeginPrompt(ctx context.Context, sess *api.Session, in promptinput.Input) {
	hasInput := strings.TrimSpace(in.Text) != "" || len(in.ContentParts) > 0 || len(in.ArtifactIDs) > 0
	if in.HostSignal == nil && !in.Continuation && !sess.IsWorkerChild() && hasInput {
		m.BeginIntent(ctx, sess.ID)
		return
	}
	m.beginPrompt(sess.ID)
}

func (m *Service) BeginIntent(ctx context.Context, sessionID string) {
	rootID := sessiontree.RootID(ctx, m.store, sessionID)
	if rootID == sessionID {
		m.beginIntent(rootID)
	} else {
		m.beginPrompt(sessionID)
	}
}

// BeginWorkflowPhase restarts the session tree's grounding friction for a
// newly entered workflow phase.
func (m *Service) BeginWorkflowPhase(ctx context.Context, sessionID string) {
	if m == nil {
		return
	}
	m.beginPhase(sessiontree.RootID(ctx, m.store, sessionID))
}

// RecordGroundingFriction shares cycle exhaustion across coordinator and worker rejects.
func (m *Service) RecordGroundingFriction(ctx context.Context, sessionID string) guidance.GroundingFriction {
	if m == nil {
		return guidance.GroundingFriction{Remaining: limits.DefaultGroundingRejectsPerCycle}
	}
	return m.recordFriction(sessionID, sessiontree.RootID(ctx, m.store, sessionID))
}

func New(store Store) *Service { return &Service{store: store} }
