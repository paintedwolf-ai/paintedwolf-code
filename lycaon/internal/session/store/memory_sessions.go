package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/pkg/api"
)

// Create stores a new session with resolved project identity.
func (s *memorySessions) Create(ctx context.Context, req api.CreateSessionRequest, projectID string) (*api.Session, error) {
	return s.CreateWithStatus(ctx, req, projectID, api.SessionStatusIdle)
}

// CreateWithStatus stores a session in a valid initial state.
func (s *memorySessions) CreateWithStatus(ctx context.Context, req api.CreateSessionRequest, projectID string, status api.SessionStatus) (*api.Session, error) {
	if !api.IsInitialSessionStatus(status) {
		return nil, &SessionStatusTransitionError{To: status}
	}
	owner, err := people.Acting(ctx, s.core)
	if err != nil {
		return nil, err
	}
	return createMemorySession(ctx, s.core, owner.ID, req, projectID, "", "", 0, status)
}

// CreateChild stores a child session linked to a parent coordinator session.
func (s *memorySessions) CreateChild(ctx context.Context, parent *api.Session, req api.SpawnChildRequest) (*api.Session, error) {
	if parent == nil {
		return nil, fmt.Errorf("parent session required")
	}
	req.AgentType = strings.TrimSpace(req.AgentType)
	if req.AgentType == "" {
		return nil, fmt.Errorf("agent_type is required")
	}
	mode := ChildSessionPosture(parent.Posture, req.AgentType)
	childReq := api.CreateSessionRequest{
		ProjectID:       parent.ProjectID,
		WorkspaceRootID: parent.WorkspaceRootID,
		Posture:         mode,
	}
	sess, err := createMemorySession(ctx, s.core, parent.OwnerPersonID, childReq, parent.ProjectID, parent.ID, req.AgentType, req.MaxToolLoops, api.SessionStatusIdle)
	if err != nil {
		return nil, err
	}
	if path := strings.TrimSpace(parent.WorkspacePath); path != "" {
		sess.WorkspacePath = path
	}
	if strings.TrimSpace(req.Prompt) != "" {
		userMsg := api.Message{
			ID:        uuid.NewString(),
			Role:      api.MessageRoleUser,
			Content:   req.Prompt,
			WorkerID:  strings.TrimSpace(req.WorkerJobID),
			CreatedAt: time.Now().UTC(),
		}
		if err := (&memoryTranscript{core: s.core}).AppendMessages(ctx, sess.ID, userMsg); err != nil {
			return nil, err
		}
	}
	return sess, nil
}

func createMemorySession(ctx context.Context, s *memoryCore, ownerPersonID string, req api.CreateSessionRequest, projectID, parentID, agentType string, maxToolLoops int, status api.SessionStatus) (*api.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ownerPersonID == "" {
		return nil, ErrSessionOwnerRequired
	}
	providerID, model, err := normalizeSessionModelRef(req.ProviderID, req.Model)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	posture := req.Posture
	if strings.TrimSpace(string(posture)) == "" {
		posture = api.SessionPostureBuild
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = strings.TrimSpace(req.ProjectID)
	}
	agentType = strings.TrimSpace(agentType)
	if agentType == "" {
		agentType = defaultRootSessionAgentType(parentID)
	}
	sess := &api.Session{
		ID:              uuid.New().String(),
		ProjectID:       projectID,
		OwnerPersonID:   ownerPersonID,
		WorkspaceRootID: strings.TrimSpace(req.WorkspaceRootID),
		Posture:         posture,
		AgentType:       agentType,
		ProviderID:      providerID,
		Model:           model,
		Status:          status,
		ParentSessionID: parentID,
		MaxToolLoops:    maxToolLoops,
		CreatedAt:       now,
		ActivityAt:      now,
		UpdatedAt:       now,
	}

	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.mu.Unlock()

	return sess, nil
}

// Get returns a session by ID.
func (s *memorySessions) Get(ctx context.Context, id string) (*api.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	core := s.core
	core.mu.RLock()
	defer core.mu.RUnlock()

	sess, ok := core.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	sessCopy := *sess
	sessCopy.UntrustedContent = core.untrustedRecords[id] > 0
	sessCopy.CurrentTurn = currentUserTurnLocked(core, id)
	return &sessCopy, nil
}

// List returns all sessions.
func (s *memorySessions) List(ctx context.Context) ([]*api.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	core := s.core
	core.mu.RLock()
	defer core.mu.RUnlock()

	out := make([]*api.Session, 0, len(core.sessions))
	for id, sess := range core.sessions {
		copy := *sess
		copy.UntrustedContent = core.untrustedRecords[id] > 0
		copy.CurrentTurn = currentUserTurnLocked(core, id)
		out = append(out, &copy)
	}
	return out, nil
}

// ListBusySessionIDs returns one deterministic recovery page.
func (s *memorySessions) ListBusySessionIDs(ctx context.Context, limit int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []string{}, nil
	}
	core := s.core
	core.mu.RLock()
	defer core.mu.RUnlock()
	ids := make([]string, 0)
	for id, sess := range core.sessions {
		if sess != nil && sess.Status == api.SessionStatusBusy {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

// Delete removes a session and its child tree.
func (s *memorySessions) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	core := s.core
	core.mu.Lock()
	defer core.mu.Unlock()

	if _, ok := core.sessions[id]; !ok {
		return ErrSessionNotFound
	}
	for _, treeID := range memorySessionTreeIDs(core.sessions, id) {
		deleteSessionLocked(core, treeID)
	}
	return nil
}

func deleteSessionLocked(s *memoryCore, id string) {
	delete(s.sessions, id)
	delete(s.turnHeads, id)
	delete(s.statusChangedAt, id)
	delete(s.modelLimits, id)
	delete(s.messages, id)
	delete(s.turnClocks, id)
	delete(s.turnLoads, id)
	delete(s.transcriptSeq, id)
	delete(s.transcriptOrd, id)
	delete(s.userTurns, id)
	delete(s.userTurnSequence, id)
	delete(s.turnSourceBriefs, id)
	delete(s.screenGeneration, id)
	delete(s.evidence, id)
	delete(s.untrustedRecords, id)
	delete(s.secretExposureRecords, id)
	delete(s.compactionViews, id)
	delete(s.chunkProjections, id)
	delete(s.compactionAttempts, id)
	delete(s.worktreeBindings, id)
	for key := range s.draftVersions {
		if strings.HasPrefix(key, id+"\x00") {
			delete(s.draftVersions, key)
		}
	}
}

func memorySessionTreeIDs(sessions map[string]*api.Session, rootID string) []string {
	children := map[string][]string{}
	for _, sess := range sessions {
		if sess == nil {
			continue
		}
		parent := strings.TrimSpace(sess.ParentSessionID)
		if parent == "" {
			continue
		}
		children[parent] = append(children[parent], sess.ID)
	}
	out := []string{rootID}
	queue := []string{rootID}
	seen := map[string]struct{}{rootID: {}}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, child := range children[id] {
			if _, ok := seen[child]; ok {
				continue
			}
			seen[child] = struct{}{}
			out = append(out, child)
			queue = append(queue, child)
		}
	}
	return out
}
