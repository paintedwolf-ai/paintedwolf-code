package session

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/pkg/api"
)

// closeoutLifecycle keeps prompt budgets separate from shared root-cycle state.
// Its lock makes updates, resets, and eviction atomic.
type closeoutLifecycle struct {
	mu      sync.Mutex
	prompts scopedstore.LRU[closeoutPromptState]
	cycles  scopedstore.LRU[closeoutCycleState]
}

type closeoutPromptState struct {
	rootID           string
	delays           [closeoutDelayKinds]int
	groundingRejects int
}

type closeoutCycleState struct {
	groundingRejects int
	stall            closeoutStall
}

type closeoutDelayKind int

const (
	closeoutProgressDelay closeoutDelayKind = iota
	closeoutVerdictDelay
	closeoutGateDelay
	closeoutDelayKinds
)

func (c *closeoutLifecycle) beginPrompt(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prompts.Delete(sessionID)
}

func (c *closeoutLifecycle) beginIntent(rootID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prompts.Delete(rootID)
	c.cycles.Delete(rootID)
}

// beginPhase restarts the root cycle's grounding friction when a workflow
// enters a new phase, so rejects spent in one phase do not exhaust the next.
// Same-phase re-entry continues the cycle.
func (c *closeoutLifecycle) beginPhase(rootID string) {
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
func (c *closeoutLifecycle) forget(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forgetLocked(sessionID)
}

func (c *closeoutLifecycle) forgetLocked(sessionID string) {
	c.prompts.Delete(sessionID)
	c.cycles.Delete(sessionID)
	for id, prompt := range c.prompts.Snapshot() {
		if prompt.rootID == sessionID {
			c.prompts.Delete(id)
		}
	}
}

func (c *closeoutLifecycle) rewind(sessionID, rootID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forgetLocked(sessionID)
	if rootID != sessionID {
		c.forgetLocked(rootID)
	}
}

// delay returns the pre-attempt count and charges only the named obligation.
func (c *closeoutLifecycle) delay(sessionID, rootID string, kind closeoutDelayKind, eligible bool, limit int) (count int, delayed bool) {
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

func (c *closeoutLifecycle) recordFriction(sessionID, rootID string) guidance.GroundingFriction {
	c.mu.Lock()
	defer c.mu.Unlock()
	prompt, _ := c.prompts.Load(sessionID)
	return c.recordFrictionLocked(sessionID, rootID, prompt)
}

func (c *closeoutLifecycle) recordFrictionLocked(sessionID, rootID string, prompt closeoutPromptState) guidance.GroundingFriction {
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

func (m *Manager) beginCloseoutPrompt(ctx context.Context, sess *api.Session, in PromptInput) {
	hasInput := strings.TrimSpace(in.Text) != "" || len(in.ContentParts) > 0 || len(in.ArtifactIDs) > 0
	if in.HostSignal == nil && !sess.IsWorkerChild() && hasInput {
		m.beginCloseoutIntent(ctx, sess.ID)
		return
	}
	m.closeout.beginPrompt(sess.ID)
}

func (m *Manager) beginCloseoutIntent(ctx context.Context, sessionID string) {
	rootID := RootSessionID(ctx, m.store, sessionID)
	if rootID == sessionID {
		m.closeout.beginIntent(rootID)
	} else {
		m.closeout.beginPrompt(sessionID)
	}
}

// BeginWorkflowPhase restarts the session tree's grounding friction for a
// newly entered workflow phase.
func (m *Manager) BeginWorkflowPhase(ctx context.Context, sessionID string) {
	if m == nil {
		return
	}
	m.closeout.beginPhase(RootSessionID(ctx, m.store, sessionID))
}

// RecordGroundingFriction shares cycle exhaustion across coordinator and worker rejects.
func (m *Manager) RecordGroundingFriction(ctx context.Context, sessionID string) guidance.GroundingFriction {
	if m == nil {
		return guidance.GroundingFriction{Remaining: limits.DefaultGroundingRejectsPerCycle}
	}
	return m.closeout.recordFriction(sessionID, RootSessionID(ctx, m.store, sessionID))
}
