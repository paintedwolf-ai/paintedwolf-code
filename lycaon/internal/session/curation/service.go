package curation

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/observability"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type Store interface {
	Get(context.Context, string) (*wire.Session, error)
}
type Roots interface {
	SettingsRoots(context.Context, *wire.Session) []string
}
type Policy interface {
	GetForProjectRoots([]string) (llm.ModelPolicy, error)
}
type Providers interface {
	Get(string) (modelcall.Provider, error)
}
type Admission interface {
	WithSessionTreeAdmission(context.Context, string, func() error) error
}
type Naming interface {
	SessionFromPrompt(context.Context, *wire.Session, string)
	ProjectFromPrompt(context.Context, *wire.Session, string)
}
type Warming interface {
	DeclaredURLs(context.Context, string, string, string, string)
}

type Service struct {
	store     Store
	roots     Roots
	policy    Policy
	providers Providers
	admission Admission
	naming    Naming
	warming   Warming
	work      promptCurations
}

func New(store Store, roots Roots, admission Admission, naming Naming, warming Warming) *Service {
	return &Service{store: store, roots: roots, admission: admission, naming: naming, warming: warming}
}
func (s *Service) SetModelSources(policy Policy, providers Providers) {
	s.policy, s.providers = policy, providers
}
func (s *Service) Cancel(sessionID string) {
	if s != nil {
		s.work.Cancel(sessionID)
	}
}
func (s *Service) Stop() {
	if s != nil {
		s.work.Stop()
	}
}

// SharesCoordinatorCapacity reports whether curation shares coordinator capacity.
func (m *Service) SharesCoordinatorCapacity(ctx context.Context, sess *wire.Session) bool {
	if m == nil || m.policy == nil || m.providers == nil {
		return false
	}
	policy, err := m.policy.GetForProjectRoots(m.roots.SettingsRoots(ctx, sess))
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
	p, err := m.providers.Get(lite.ProviderID)
	if err != nil || p == nil {
		return false
	}
	return p.Profile().UtilitySingleFlight
}

func (m *Service) Begin(ctx context.Context, sess *wire.Session, userPrompt string) func() {
	if m.SharesCoordinatorCapacity(ctx, sess) {
		return func() {
			m.Kick(context.WithoutCancel(ctx), sess, userPrompt)
		}
	}
	m.Kick(ctx, sess, userPrompt)
	return func() {}
}

// AcceptedWorkflowRequest names a chat and draft project from workflow input.
// Workflow starts and request answers can bypass ordinary prompt execution.
func (m *Service) AcceptedWorkflowRequest(ctx context.Context, sessionID, text string) {
	if m == nil || m.store == nil {
		return
	}
	// A committed workflow may park and cancel its initiating turn.
	ctx = context.WithoutCancel(ctx)
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return
	}
	m.Kick(ctx, sess, text)
}

func (m *Service) Kick(ctx context.Context, sess *wire.Session, userPrompt string) {
	if m == nil || sess == nil || sess.IsWorkerChild() || strings.TrimSpace(userPrompt) == "" {
		return
	}
	var bg context.Context
	var work *curationWork
	if err := m.admission.WithSessionTreeAdmission(ctx, sess.ID, func() error {
		var cancel context.CancelFunc
		bg, cancel = context.WithCancel(curationctx.WithoutLane(context.WithoutCancel(ctx)))
		work = m.work.Register(sess.ID, cancel)
		return nil
	}); err != nil || work == nil {
		return
	}
	//nolint:contextcheck // Session stop controls this context.
	go m.run(bg, sess, userPrompt, work)
}

func (m *Service) run(ctx context.Context, sess *wire.Session, userPrompt string, work *curationWork) {
	defer m.work.Finish(sess.ID, work)
	defer observability.GuardPanic("session.prompt_curation")
	m.warming.DeclaredURLs(ctx, sess.ID, sess.ProjectID, userPrompt, sess.WorkspacePath)
	m.naming.SessionFromPrompt(ctx, sess, userPrompt)
	m.naming.ProjectFromPrompt(ctx, sess, userPrompt)
}

func (m *Service) Wait(ctx context.Context) {
	if m != nil {
		m.work.Wait(ctx)
	}
}

// promptCurations tracks in-flight prompt naming and research work per session
// so forget and stop can cancel it and shutdown can drain it.
type promptCurations struct {
	mu        sync.Mutex
	stopped   bool
	bySession map[string]map[*curationWork]struct{}
	idle      chan struct{}
}

type curationWork struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (m *promptCurations) Register(sessionID string, cancel context.CancelFunc) *curationWork {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		cancel()
		return nil
	}
	work := &curationWork{cancel: cancel, done: make(chan struct{})}
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
	return work
}

func (m *promptCurations) Stop() {
	m.mu.Lock()
	m.stopped = true
	var works []*curationWork
	for _, sessionWorks := range m.bySession {
		for work := range sessionWorks {
			works = append(works, work)
			work.cancel()
		}
	}
	m.mu.Unlock()
	for _, work := range works {
		<-work.done
	}
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
