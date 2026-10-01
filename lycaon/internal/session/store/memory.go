package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

// Memory is a thread-safe in-memory session store.
type Memory struct {
	owner         people.Person
	mu            sync.RWMutex
	checkpoints   map[string]map[string]memoryCheckpoint
	modelLimits   map[string]ModelLimit
	turnCloseouts map[string]string
	sessions      map[string]*api.Session  // sessionID -> session
	messages      map[string][]api.Message // sessionID -> transcript
	// statusChangedAt holds when a session's status last changed; absent means
	// unchanged since creation.
	statusChangedAt map[string]time.Time
	// transcriptSeq is the session mutation clock.
	transcriptSeq map[string]int64
	// transcriptOrd is the stable message creation ordinal.
	transcriptOrd    map[string]int64
	userTurns        map[string]map[string]int
	userTurnSequence map[string]int
	// turnSourceBriefs maps session → opening message → brief JSON.
	turnSourceBriefs map[string]map[string]string
	// screenGeneration is the evidence revision each row was screened at.
	screenGeneration      map[string]map[string]uint64
	evidence              map[string]map[string]memoryEvidenceRow
	artifactHandles       visual.HandleBinder
	untrustedRecords      map[string]int // sessionID → qualifying live record count (cache)
	secretExposureRecords map[string]int // sessionID → secret-exposure marker count (cache)
	compactionAttempts    map[string]messageview.CompactionAttempt
	compactionViews       map[string]CompactionView
	chunkProjections      map[string]map[string]messageview.ChunkProjection
	draftVersions         map[string][]api.DraftVersion
	worktreeBindings      map[string]WorktreeBinding
	worktreeIdentities    map[string]string
	promptSubmissions     map[string]PromptSubmission
	promptSubmissionSeq   map[string]int64
	promptAttachmentBlobs map[string]map[string]memoryPromptAttachmentBlob
	rewindOperations      map[string]RewindOperation
	rewindCreatedAt       map[string]time.Time
	turns                 map[string]Turn
	turnHeads             map[string]string
	turnAttempts          map[string][]TurnAttempt
	liveModelOutputs      map[string]LiveModelOutput
	modelOutputs          map[string]ModelOutput
	turnSubmissions       map[string]string
	workerTurns           map[string][]string
	projectedModelOutputs map[string]int64
	// turnClocks maps session id to opening message id to clock.
	turnClocks map[string]map[string]TurnClock
	// turnLoads keeps each session's load receipts in turn order; turnLoadSeq
	// numbers them across sessions the way the SQL row id does.
	turnLoads   map[string][]TurnLoadReceipt
	turnLoadSeq int64
}

type memoryEvidenceRow struct {
	rec          evidence.Record
	supersededBy string
}

type memoryPromptAttachmentBlob struct {
	byteSize  int64
	createdAt time.Time
}

// NewMemory creates an empty in-memory session store.
func NewMemory() *Memory {
	return &Memory{
		userTurns:             make(map[string]map[string]int),
		userTurnSequence:      make(map[string]int),
		turnSourceBriefs:      make(map[string]map[string]string),
		owner:                 people.Person{ID: uuid.NewString(), Role: api.PersonRoleOwner},
		sessions:              make(map[string]*api.Session),
		statusChangedAt:       make(map[string]time.Time),
		messages:              make(map[string][]api.Message),
		transcriptSeq:         make(map[string]int64),
		transcriptOrd:         make(map[string]int64),
		screenGeneration:      make(map[string]map[string]uint64),
		evidence:              make(map[string]map[string]memoryEvidenceRow),
		untrustedRecords:      make(map[string]int),
		secretExposureRecords: make(map[string]int),
		compactionViews:       make(map[string]CompactionView),
		draftVersions:         make(map[string][]api.DraftVersion),
		worktreeBindings:      make(map[string]WorktreeBinding),
		worktreeIdentities:    make(map[string]string),
		promptSubmissions:     make(map[string]PromptSubmission),
		promptSubmissionSeq:   make(map[string]int64),
		promptAttachmentBlobs: make(map[string]map[string]memoryPromptAttachmentBlob),
		rewindOperations:      make(map[string]RewindOperation),
		rewindCreatedAt:       make(map[string]time.Time),
		turns:                 make(map[string]Turn),
		turnHeads:             make(map[string]string),
		turnAttempts:          make(map[string][]TurnAttempt),
		liveModelOutputs:      make(map[string]LiveModelOutput),
		modelOutputs:          make(map[string]ModelOutput),
		turnSubmissions:       make(map[string]string),
		workerTurns:           make(map[string][]string),
		projectedModelOutputs: make(map[string]int64),
		turnClocks:            make(map[string]map[string]TurnClock),
		turnLoads:             make(map[string][]TurnLoadReceipt),
	}
}

// MutationEventsOutboxed reports false: the memory store publishes no events.
func (*Memory) MutationEventsOutboxed() bool { return false }

// Create stores a new session with resolved project identity.
func (s *Memory) Create(ctx context.Context, req api.CreateSessionRequest, projectID string) (*api.Session, error) {
	return s.CreateWithStatus(ctx, req, projectID, api.SessionStatusIdle)
}

// CreateWithStatus stores a session in a valid initial state.
func (s *Memory) CreateWithStatus(ctx context.Context, req api.CreateSessionRequest, projectID string, status api.SessionStatus) (*api.Session, error) {
	if !api.IsInitialSessionStatus(status) {
		return nil, &SessionStatusTransitionError{To: status}
	}
	owner, err := people.Acting(ctx, s)
	if err != nil {
		return nil, err
	}
	return s.createSession(ctx, owner.ID, req, projectID, "", "", 0, status)
}

// CreateChild stores a child session linked to a parent coordinator session.
func (s *Memory) CreateChild(ctx context.Context, parent *api.Session, req api.SpawnChildRequest) (*api.Session, error) {
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
	sess, err := s.createSession(ctx, parent.OwnerPersonID, childReq, parent.ProjectID, parent.ID, req.AgentType, req.MaxToolLoops, api.SessionStatusIdle)
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
		if err := s.AppendMessages(ctx, sess.ID, userMsg); err != nil {
			return nil, err
		}
	}
	return sess, nil
}

func (s *Memory) createSession(ctx context.Context, ownerPersonID string, req api.CreateSessionRequest, projectID, parentID, agentType string, maxToolLoops int, status api.SessionStatus) (*api.Session, error) {
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
func (s *Memory) Get(ctx context.Context, id string) (*api.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	sess, ok := s.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	sessCopy := *sess
	sessCopy.UntrustedContent = s.untrustedRecords[id] > 0
	sessCopy.CurrentTurn = s.currentUserTurnLocked(id)
	return &sessCopy, nil
}

// List returns all sessions.
func (s *Memory) List(ctx context.Context) ([]*api.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*api.Session, 0, len(s.sessions))
	for id, sess := range s.sessions {
		copy := *sess
		copy.UntrustedContent = s.untrustedRecords[id] > 0
		copy.CurrentTurn = s.currentUserTurnLocked(id)
		out = append(out, &copy)
	}
	return out, nil
}

// ListBusySessionIDs returns one deterministic recovery page.
func (s *Memory) ListBusySessionIDs(ctx context.Context, limit int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []string{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0)
	for id, sess := range s.sessions {
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
func (s *Memory) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[id]; !ok {
		return ErrSessionNotFound
	}
	for _, treeID := range memorySessionTreeIDs(s.sessions, id) {
		s.deleteSessionLocked(treeID)
	}
	return nil
}

func (s *Memory) deleteSessionLocked(id string) {
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

// GetMessages returns a copy of session message history.
func (s *Memory) GetMessages(ctx context.Context, id string) ([]api.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, ok := s.sessions[id]; !ok {
		return nil, ErrSessionNotFound
	}
	msgs := s.messages[id]
	out := make([]api.Message, len(msgs))
	copy(out, msgs)
	return out, nil
}

// GetWorkerJobMessages reads only rows associated with one worker job.
func (s *Memory) GetWorkerJobMessages(ctx context.Context, sessionID, workerJobID string) ([]api.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	workerJobID = strings.TrimSpace(workerJobID)
	if workerJobID == "" {
		return nil, fmt.Errorf("worker job id required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return nil, ErrSessionNotFound
	}
	out := make([]api.Message, 0)
	for _, message := range s.messages[sessionID] {
		if strings.TrimSpace(message.WorkerID) == workerJobID {
			out = append(out, message)
		}
	}
	return out, nil
}

// GetMessage reads one transcript projection by stable identity.
func (s *Memory) GetMessage(ctx context.Context, sessionID, messageID string) (api.Message, error) {
	if err := ctx.Err(); err != nil {
		return api.Message{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return api.Message{}, ErrSessionNotFound
	}
	for _, message := range s.messages[sessionID] {
		if message.ID == messageID {
			return message, nil
		}
	}
	return api.Message{}, ErrMessageNotFound
}

// GetMessagesAfterOrd seeks through the immutable transcript order.
func (s *Memory) GetMessagesAfterOrd(ctx context.Context, sessionID string, afterOrd int64, limit int) ([]api.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []api.Message{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return nil, ErrSessionNotFound
	}
	out := make([]api.Message, 0, limit)
	for _, message := range s.messages[sessionID] {
		if message.Ord <= afterOrd {
			continue
		}
		out = append(out, message)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// GetTranscriptPage returns one Ord window plus the session's transcript watermark.
func (s *Memory) GetTranscriptPage(ctx context.Context, id string, q api.TranscriptPageQuery) (api.SessionTranscriptPage, error) {
	if err := ctx.Err(); err != nil {
		return api.SessionTranscriptPage{}, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, ok := s.sessions[id]; !ok {
		return api.SessionTranscriptPage{}, ErrSessionNotFound
	}
	all := s.messages[id]
	if workerJobID := strings.TrimSpace(q.WorkerID); workerJobID != "" {
		filtered := make([]api.Message, 0, len(all))
		for _, message := range all {
			if strings.TrimSpace(message.WorkerID) == workerJobID {
				filtered = append(filtered, message)
			}
		}
		all = filtered
	}
	window := pageAscendingWindow(all, q)
	page := api.SessionTranscriptPage{
		Messages:   window,
		TurnClocks: map[string]api.TurnClock{},
		TurnLoads:  map[string][]api.TurnLoad{},
		Watermark:  s.transcriptSeq[id],
	}
	if page.Messages == nil {
		page.Messages = []api.Message{}
	}
	if strings.TrimSpace(q.WorkerID) == "" {
		page.TurnClocks = s.turnClocksForMessagesLocked(id, page.Messages)
	}
	hasBefore, hasAfter := windowHasMore(all, page.Messages)
	// Empty window with an explicit before/after cursor: has_more relative to the cursor.
	if len(page.Messages) == 0 {
		switch {
		case q.Before != nil:
			bound := *q.Before
			hasBefore, hasAfter = false, false
			for _, m := range all {
				if m.Ord < bound {
					hasBefore = true
				}
				if m.Ord >= bound {
					hasAfter = true
				}
			}
		case q.After != nil:
			bound := *q.After
			hasBefore, hasAfter = false, false
			for _, m := range all {
				if m.Ord <= bound {
					hasBefore = true
				}
				if m.Ord > bound {
					hasAfter = true
				}
			}
		default:
			hasBefore, hasAfter = false, false
		}
	}
	if err := SetTranscriptPageCursors(id, &page, hasBefore, hasAfter, q); err != nil {
		return api.SessionTranscriptPage{}, err
	}
	return page, nil
}

// nextSeqLocked bumps the session's transcript mutation clock. Callers hold s.mu.
func (s *Memory) nextSeqLocked(sessionID string) int64 {
	s.transcriptSeq[sessionID]++
	return s.transcriptSeq[sessionID]
}

// nextOrdLocked bumps the session's immutable creation ordinal; only appends
// assign one. Callers hold s.mu.
func (s *Memory) nextOrdLocked(sessionID string) int64 {
	s.transcriptOrd[sessionID]++
	return s.transcriptOrd[sessionID]
}

// AppendMessages stamps sequence numbers into the caller's slice for publication.
func (s *Memory) AppendMessages(ctx context.Context, id string, msgs ...api.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	if err := rejectDuplicateAppendIDs(msgs); err != nil {
		return err
	}
	if err := rejectExistingMessageIDs(s.messages[id], msgs); err != nil {
		return err
	}
	screenMessagesForStore(messageScreenContext(ctx, sess.ProjectID, id), msgs)
	for i := range msgs {
		msgs[i] = api.NormalizeMessageProvenance(msgs[i])
		if err := requireWorkflowRunStamp(msgs[i]); err != nil {
			return err
		}
		msgs[i].Seq = s.nextSeqLocked(id)
		msgs[i].Ord = s.nextOrdLocked(id)
		if api.IsUserIntentMessage(msgs[i]) {
			if s.userTurns[id] == nil {
				s.userTurns[id] = make(map[string]int)
			}
			s.userTurnSequence[id]++
			s.userTurns[id][msgs[i].ID] = s.userTurnSequence[id]
		}
	}
	s.messages[id] = append(s.messages[id], msgs...)
	sess.UpdatedAt = time.Now().UTC()
	if appendsActivity(msgs) {
		sess.ActivityAt = sess.UpdatedAt
	}
	return nil
}

// PutCompactionView atomically publishes a fresh view and its next generation.
func (s *Memory) PutCompactionView(ctx context.Context, sessionID string, view CompactionView) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return ErrSessionNotFound
	}
	if view.Generation != s.sessions[sessionID].CompactionGeneration+1 {
		return fmt.Errorf("compaction generation changed")
	}
	boundaryFound := view.CoveredThroughOrd == 0 && strings.TrimSpace(view.CoveredThroughID) == ""
	for _, message := range s.messages[sessionID] {
		if message.Ord <= view.CoveredThroughOrd && message.Seq > view.SourceSeq {
			return fmt.Errorf("compaction source changed")
		}
		if message.Ord == view.CoveredThroughOrd && message.ID == view.CoveredThroughID {
			boundaryFound = true
		}
	}
	if !boundaryFound {
		return fmt.Errorf("compaction boundary is not a live session message")
	}
	cp := view
	cp.Messages = append([]api.Message(nil), view.Messages...)
	cp.CreatedAt = time.Now().UTC()
	s.compactionViews[sessionID] = cp
	s.sessions[sessionID].CompactionGeneration = view.Generation
	s.sessions[sessionID].UpdatedAt = cp.CreatedAt
	return nil
}

// GetCompactionView returns the session's compacted view, ok=false when none exists.
func (s *Memory) GetCompactionView(ctx context.Context, sessionID string) (*CompactionView, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.compactionViews[sessionID]
	if !ok {
		return nil, false, nil
	}
	cp := v
	cp.Messages = append([]api.Message(nil), v.Messages...)
	return &cp, true, nil
}

// CompactionViewCurrent reports whether a covered row changed after the view.
func (s *Memory) CompactionViewCurrent(ctx context.Context, sessionID string, coveredThroughOrd int64, coveredThroughID string, sourceSeq int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return false, ErrSessionNotFound
	}
	boundaryFound := coveredThroughOrd == 0
	for _, message := range s.messages[sessionID] {
		if message.Ord == coveredThroughOrd && message.ID == coveredThroughID {
			boundaryFound = true
		}
		if message.Ord <= coveredThroughOrd && message.Seq > sourceSeq {
			return false, nil
		}
	}
	return boundaryFound, nil
}

// UpdateMessage replaces one message by ID.
func (s *Memory) UpdateMessage(ctx context.Context, sessionID, messageID string, msg api.Message) (api.Message, error) {
	if err := ctx.Err(); err != nil {
		return api.Message{}, err
	}
	msg = api.NormalizeMessageProvenance(msg)
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return api.Message{}, ErrSessionNotFound
	}
	msg = screenMessageForStore(messageScreenContext(ctx, sess.ProjectID, sessionID), msg)
	msgs := s.messages[sessionID]
	for i := range msgs {
		if msgs[i].ID == messageID {
			msg.Seq = s.nextSeqLocked(sessionID)
			// Patches retain the original ordinal, timestamp, and author.
			msg.Ord = msgs[i].Ord
			msg.CreatedAt = msgs[i].CreatedAt
			msg.AuthorPersonID = msgs[i].AuthorPersonID
			msgs[i] = msg
			s.messages[sessionID] = msgs
			if view, ok := s.compactionViews[sessionID]; ok && msg.Ord <= view.CoveredThroughOrd {
				delete(s.compactionViews, sessionID)
			}
			sess.UpdatedAt = time.Now().UTC()
			return msg, nil
		}
	}
	return api.Message{}, fmt.Errorf("%w: %s", ErrMessageNotFound, messageID)
}

// PatchLiveProjection writes streaming prose and in-flight tool_calls. Seq is unchanged.
func (s *Memory) PatchLiveProjection(ctx context.Context, sessionID, messageID, content string, toolCalls []api.ToolCall) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return ErrSessionNotFound
	}
	msgs := s.messages[sessionID]
	for i := range msgs {
		if msgs[i].ID == messageID {
			msgs[i].Content = content
			if toolCalls != nil {
				msgs[i].ToolCalls = append([]api.ToolCall(nil), toolCalls...)
			}
			s.messages[sessionID] = msgs
			if view, ok := s.compactionViews[sessionID]; ok && msgs[i].Ord <= view.CoveredThroughOrd {
				delete(s.compactionViews, sessionID)
			}
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrMessageNotFound, messageID)
}

// UpdateSession mutates session fields under store lock.
func (s *Memory) UpdateSession(ctx context.Context, id string, fn func(*api.Session)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	updated := *sess
	fn(&updated)
	if !api.CanTransitionSessionStatus(sess.Status, updated.Status) {
		return &SessionStatusTransitionError{From: sess.Status, To: updated.Status}
	}
	// Pins and activity have their own writers; archiving or moving project clears the pin.
	updated.ActivityAt = sess.ActivityAt
	updated.PinRank = sess.PinRank
	if updated.ProjectID != sess.ProjectID || updated.ArchivedAt != nil {
		updated.PinRank = nil
	}
	updated.UpdatedAt = time.Now().UTC()
	if updated.Status != sess.Status {
		s.statusChangedAt[id] = updated.UpdatedAt
	}
	*sess = updated
	return nil
}

// UpdateTitleIfUnset sets title when still blank; reports whether a row was updated.
func (s *Memory) UpdateTitleIfUnset(ctx context.Context, id, title string) (bool, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return false, ErrSessionNotFound
	}
	if strings.TrimSpace(sess.Title) != "" {
		return false, nil
	}
	sess.Title = title
	sess.UpdatedAt = time.Now().UTC()
	return true, nil
}

// ReassignSessionsWorkspaceRoot retargets sessions from one root.
func (s *Memory) ReassignSessionsWorkspaceRoot(ctx context.Context, projectID, fromRootID, toRootID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	projectID = strings.TrimSpace(projectID)
	fromRootID = strings.TrimSpace(fromRootID)
	if projectID == "" || fromRootID == "" {
		return nil
	}
	toRootID = strings.TrimSpace(toRootID)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for _, sess := range s.sessions {
		if sess == nil || sess.ProjectID != projectID || sess.WorkspaceRootID != fromRootID {
			continue
		}
		sess.WorkspaceRootID = toRootID
		sess.WorkspacePath = ""
		sess.UpdatedAt = now
	}
	return nil
}

// SetSessionStatusEvent updates status; the memory store publishes no events, so
// the host error is dropped.
func (s *Memory) SetSessionStatusEvent(
	ctx context.Context,
	id string,
	status api.SessionStatus,
	_ *api.SessionHostError,
) error {
	return s.SetSessionStatus(ctx, id, status)
}

// SetSessionStatus updates session status and updated_at.
func (s *Memory) SetSessionStatus(ctx context.Context, id string, status api.SessionStatus) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	if !api.CanTransitionSessionStatus(sess.Status, status) {
		return &SessionStatusTransitionError{From: sess.Status, To: status}
	}
	sess.UpdatedAt = time.Now().UTC()
	if status != sess.Status {
		s.statusChangedAt[id] = sess.UpdatedAt
	}
	sess.Status = status
	return nil
}

// HasActiveProjectSessions reports whether a project has unarchived active sessions in memory.
func (s *Memory) HasActiveProjectSessions(ctx context.Context, projectID string, activeSince time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if s == nil {
		return false, nil
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sess := range s.sessions {
		if sess.ProjectID != projectID || sess.ArchivedAt != nil {
			continue
		}
		if sess.Status == api.SessionStatusBusy || sess.ActivityAt.After(activeSince) {
			return true, nil
		}
	}
	for _, turn := range s.turns {
		if turn.ProjectID == projectID && turn.Status == TurnStatusRunning {
			return true, nil
		}
	}
	return false, nil
}
