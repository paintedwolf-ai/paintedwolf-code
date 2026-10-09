package session

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/observability"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// curationSharesLocalCoordinator reports whether curation shares coordinator capacity.
func (m *Manager) curationSharesLocalCoordinator(ctx context.Context, sess *wire.Session) bool {
	if m == nil || m.llmSvc == nil || m.llmSvc.Policy == nil || m.llmSvc.Registry == nil {
		return false
	}
	policy, err := m.llmSvc.Policy.GetForProjectRoots(m.overlayRootPaths(ctx, sess))
	if err != nil {
		return false
	}
	lite := llm.SummarizerRef(policy)
	coordID := strings.TrimSpace(policy.Coordinator.ProviderID)
	if sess != nil {
		if override := strings.TrimSpace(sess.ProviderID); override != "" {
			coordID = override
		}
	}
	if lite.ProviderID == "" || coordID == "" || lite.ProviderID != coordID {
		return false
	}
	p, err := m.llmSvc.Registry.Get(lite.ProviderID)
	if err != nil || p == nil {
		return false
	}
	return p.Profile().UtilitySingleFlight
}

func (m *Manager) beginPromptCuration(ctx context.Context, sess *wire.Session, userPrompt string) func() {
	if m.curationSharesLocalCoordinator(ctx, sess) {
		return func() {
			m.kickPromptCuration(context.WithoutCancel(ctx), sess, userPrompt)
		}
	}
	m.kickPromptCuration(ctx, sess, userPrompt)
	return func() {}
}

// CurateAcceptedWorkflowRequest names a chat and draft project from workflow input.
// Workflow starts and request answers can bypass ordinary prompt execution.
func (m *Manager) CurateAcceptedWorkflowRequest(ctx context.Context, sessionID, text string) {
	if m == nil || m.store == nil {
		return
	}
	// A committed workflow may park and cancel its initiating turn.
	ctx = context.WithoutCancel(ctx)
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return
	}
	m.kickPromptCuration(ctx, sess, text)
}

func (m *Manager) kickPromptCuration(ctx context.Context, sess *wire.Session, userPrompt string) {
	if m == nil || sess == nil || sess.IsWorkerChild() || strings.TrimSpace(userPrompt) == "" {
		return
	}
	var bg context.Context
	var work *curationWork
	var finish func()
	if err := m.WithSessionTreeAdmission(ctx, sess.ID, func() error {
		owned, done, err := m.engineWork.Begin(curationctx.WithoutLane(context.WithoutCancel(ctx)))
		if err != nil {
			return err
		}
		finish = done
		var cancel context.CancelFunc
		bg, cancel = context.WithCancel(owned)
		work = m.curation.Register(sess.ID, cancel)
		return nil
	}); err != nil {
		return
	}
	//nolint:contextcheck // Session and engine stop control this context.
	go m.runPromptCuration(bg, sess, userPrompt, work, finish)
}

// runPromptCuration releases its engine lease only after the session registry.
func (m *Manager) runPromptCuration(ctx context.Context, sess *wire.Session, userPrompt string, work *curationWork, releaseEngine func()) {
	defer releaseEngine()
	defer m.curation.Finish(sess.ID, work)
	defer observability.GuardPanic("session.prompt_curation")
	m.warmIndexForDeclaredURLs(ctx, sess.ID, sess.ProjectID, userPrompt, sess.WorkspacePath)
	m.autoTitleSessionFromPrompt(ctx, sess, userPrompt)
	m.autoNameProjectFromPrompt(ctx, sess, userPrompt)
}

func (m *Manager) WaitForPromptCuration(ctx context.Context) {
	if m != nil {
		m.curation.Wait(ctx)
	}
}

// promptCurations tracks in-flight prompt naming and research work per session
// so forget and stop can cancel it and shutdown can drain it.
type promptCurations struct {
	mu        sync.Mutex
	bySession map[string]map[*curationWork]struct{}
	idle      chan struct{}
}

type curationWork struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (m *promptCurations) Register(sessionID string, cancel context.CancelFunc) *curationWork {
	work := &curationWork{cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	if m.bySession == nil {
		m.bySession = make(map[string]map[*curationWork]struct{})
	}
	if len(m.bySession) == 0 {
		m.idle = make(chan struct{})
	}
	if m.bySession[sessionID] == nil {
		m.bySession[sessionID] = make(map[*curationWork]struct{})
	}
	m.bySession[sessionID][work] = struct{}{}
	m.mu.Unlock()
	return work
}

func (m *promptCurations) Finish(sessionID string, work *curationWork) {
	if work == nil {
		return
	}
	m.mu.Lock()
	delete(m.bySession[sessionID], work)
	if len(m.bySession[sessionID]) == 0 {
		delete(m.bySession, sessionID)
	}
	close(work.done)
	if len(m.bySession) == 0 {
		close(m.idle)
	}
	m.mu.Unlock()
	work.cancel()
}

func (m *promptCurations) Cancel(sessionID string) {
	m.mu.Lock()
	works := make([]*curationWork, 0, len(m.bySession[sessionID]))
	for work := range m.bySession[sessionID] {
		works = append(works, work)
		work.cancel()
	}
	m.mu.Unlock()
	for _, work := range works {
		<-work.done
	}
}

// Wait drains in-flight prompt curation.
func (m *promptCurations) Wait(ctx context.Context) {
	if m == nil {
		return
	}
	m.mu.Lock()
	done := m.idle
	idle := len(m.bySession) == 0
	m.mu.Unlock()
	if !idle {
		// A new busy interval cannot invalidate an earlier waiter's completion.
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
}
