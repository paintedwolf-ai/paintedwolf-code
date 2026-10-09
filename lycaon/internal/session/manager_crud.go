package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"time"
)

// CreateForProject opens a project-bound session.
func (m *Manager) CreateForProject(ctx context.Context, projectID string, mode api.SessionPosture) (*api.Session, error) {
	projectID = strings.TrimSpace(projectID)
	sess, err := m.store.Create(ctx, api.CreateSessionRequest{
		ProjectID: projectID,
		Posture:   mode,
	}, projectID)
	if err != nil {
		return nil, err
	}
	m.WarmAgentsMD(ctx, sess)
	return sess, nil
}

// Get returns a session by ID.
func (m *Manager) Get(ctx context.Context, id string) (*api.Session, error) {
	return m.store.Get(ctx, id)
}

// GetMessages returns stored message history with runtime live status stamped.
func (m *Manager) GetMessages(ctx context.Context, id string) ([]api.Message, error) {
	msgs, err := m.store.GetMessages(ctx, id)
	if err != nil {
		return nil, err
	}
	return m.Streams().StampMessages(id, msgs), nil
}

// GetWorkerJobMessages returns one worker run's transcript.
func (m *Manager) GetWorkerJobMessages(ctx context.Context, sessionID, workerJobID string) ([]api.Message, error) {
	msgs, err := m.store.GetWorkerJobMessages(ctx, sessionID, workerJobID)
	if err != nil {
		return nil, err
	}
	return m.Streams().StampMessages(sessionID, msgs), nil
}

// GetTranscriptPage returns one redacted transcript window.
func (m *Manager) GetTranscriptPage(ctx context.Context, id string, q api.TranscriptPageQuery) (api.SessionTranscriptPage, error) {
	m.reconcileOrphanedWorkflowRuns(ctx, id)
	page, err := m.store.GetTranscriptPage(ctx, id, q)
	if err != nil {
		return page, err
	}
	page.Messages = messageview.TranscriptMessages(m.Streams().StampMessages(id, page.Messages))
	page, err = messageview.BoundTranscriptPage(page, q.After != nil, func(ord int64) (string, error) {
		return store.MessagePages.Encode(pagecursor.Scope(id), store.MessagePosition{Ord: ord})
	})
	if err != nil {
		return page, err
	}
	return m.withPageTurnFacts(ctx, page)
}

// withPageTurnFacts keeps the clocks and receipts of the turns the bounded page
// still opens.
func (m *Manager) withPageTurnFacts(ctx context.Context, page api.SessionTranscriptPage) (api.SessionTranscriptPage, error) {
	opened := make(map[string]struct{}, len(page.Messages))
	for _, msg := range page.Messages {
		opened[msg.ID] = struct{}{}
	}
	for openingID := range page.TurnClocks {
		if _, ok := opened[openingID]; !ok {
			delete(page.TurnClocks, openingID)
		}
	}
	loads, err := m.TurnLoadsForPage(ctx, page)
	if err != nil {
		return page, err
	}
	page.TurnLoads = map[string][]api.TurnLoad{}
	for _, load := range loads {
		if load.OpeningMessageID != "" {
			page.TurnLoads[load.OpeningMessageID] = append(page.TurnLoads[load.OpeningMessageID], load)
		}
	}
	return page, nil
}

func sessionProjectKey(sess *api.Session) string {
	if sess == nil {
		return ""
	}
	return strings.TrimSpace(sess.ProjectID)
}

// checkSpendCeiling enforces the root-session spend rollup.
func (m *Manager) checkSpendCeiling(ctx context.Context, sessionID string, sess *api.Session) error {
	st, err := m.spendCeilingState(ctx, sessionID, sess)
	if err != nil {
		return err
	}
	if st.Reached {
		return st.reached()
	}
	return nil
}

// spendCeilingScopeID resolves the root billing scope.
func (m *Manager) spendCeilingScopeID(ctx context.Context, sessionID string, sess *api.Session) string {
	if sess != nil && strings.TrimSpace(sess.ParentSessionID) == "" {
		return sessionID
	}
	if root := RootSessionID(ctx, m.store, sessionID); strings.TrimSpace(root) != "" {
		return root
	}
	return sessionID
}

func (m *Manager) reconcileOrphanedWorkflowRuns(ctx context.Context, sessionID string) {
	if m == nil || m.workflows == nil {
		return
	}
	_ = m.workflows.Recovery.ReconcileOrphanedRuns(ctx, sessionID)
}

// HasActiveProjectSessions reports whether a project has unarchived active sessions.
func (m *Manager) HasActiveProjectSessions(ctx context.Context, projectID string, activeSince time.Time) (bool, error) {
	if m == nil || m.store == nil {
		return false, nil
	}
	return m.store.HasActiveProjectSessions(ctx, projectID, activeSince)
}
