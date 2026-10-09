package inputs

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/visual"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/publication"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
	"strings"
	"sync"
	"time"
)

// MaxAskUserPromptRunes caps ask_user prompt length.
const MaxAskUserPromptRunes = 800

// MaxAskUserOptions caps choice options.
const MaxAskUserOptions = 6

const maxAskSecretAgentUseLifetimeSeconds = int64((365 * 24 * time.Hour) / time.Second)

const (
	askUserPhasePrefix = "ask-"
	askUserCompareMin  = 2
	askUserCompareMax  = 4
)

var askUserCompareLabels = []string{"A", "B", "C", "D"}
var askUserReviewOptions = []string{"Approve", "Request changes", "Reject"}

// UserInputRequest is the host input for RequestUserInput.
type UserInputRequest struct {
	Prompt       string
	Purpose      string
	ResponseType workflowdef.FeedbackResponseType
	Options      []string
	// Artifacts identify the material under review.
	Artifacts []string
	// ToolCallID identifies the ask operation.
	ToolCallID string
	// Secret describes protected storage when ResponseType is secret.
	Secret *workflowdef.SecretInputSpec
	// WorkspaceImages reads artifact refs that name project files.
	WorkspaceImages WorkspaceImageReader
}

// UserInputHandle is returned after a successful ask_user pending open.
type UserInputHandle struct {
	PhaseID        string
	Prompt         string
	ResponseType   workflowdef.FeedbackResponseType
	Purpose        string
	RunID          string
	IssuedRevision int64
	Secret         *workflowdef.SecretInputSpec
}

// AskUserReject carries a structured ask rejection.
type AskUserReject struct {
	Code string
	Data map[string]any
}

func (e *AskUserReject) Error() string {
	if e == nil {
		return "ask_user rejected"
	}
	return string(e.Code)
}

func askUserReject(code string, data map[string]any) error {
	return &AskUserReject{Code: code, Data: data}
}

// RequestUserInput parks the active leaf on one durable ask.
func (m *Asks) RequestUserInput(ctx context.Context, sessionID string, req UserInputRequest) (UserInputHandle, error) {
	if m == nil {
		return UserInputHandle{}, fmt.Errorf("workflow manager not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return UserInputHandle{}, fmt.Errorf("session_id required")
	}

	norm, err := normalizeAskUserRequest(req)
	if err != nil {
		return UserInputHandle{}, err
	}
	inputDigest, err := askUserInputDigest(norm)
	if err != nil {
		return UserInputHandle{}, err
	}
	guard := m.Guard(sessionID)
	guard.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			guard.Unlock()
		}
	}()

	run, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil {
		return UserInputHandle{}, err
	}
	if run == nil {
		return UserInputHandle{}, askUserReject("ASK_USER_NO_ACTIVE_RUN", nil)
	}
	if approval := m.Approvals.InheritedApprovalSnapshot(ctx, run); approval != nil {
		return UserInputHandle{}, askUserReject("ASK_USER_INHERITED_BLUEPRINT_FORBIDDEN", map[string]any{
			"blueprint_status":           approval.Status,
			"inherited_blueprint_status": approval.Status, "inherited_blueprint_approved": approval.Status == "approved",
			"inherited_parent_run": approval.ParentRunID,
			"parent_run_id":        approval.ParentRunID,
		})
	}

	unlockVars := m.Vars.Lock(run.ID)
	varsUnlocked := false
	defer func() {
		if !varsUnlocked {
			unlockVars()
		}
	}()
	// Admission checks run before any artifact is copied, so a refused or
	// replayed ask leaves no stored image behind.
	vars, err := m.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return UserInputHandle{}, err
	}
	if handle, ok, replayErr := replayUserInputFromVars(vars, run.ID, req.ToolCallID, inputDigest); replayErr != nil || ok {
		return handle, replayErr
	}
	if scaffoldvars.HasPendingUserInput(vars) {
		return UserInputHandle{}, askUserReject("ASK_USER_ALREADY_PENDING", pendingAskRejectData(vars))
	}
	attached, err := m.resolveAskArtifacts(ctx, sessionID, norm.artifactRefs, req.WorkspaceImages)
	if err != nil {
		return UserInputHandle{}, err
	}
	norm.attach(attached.ids)

	phaseID := askUserPhasePrefix + uuid.NewString()
	var replayed UserInputHandle
	replayHit := false
	stamped, err := m.Vars.StampLocked(ctx, run.ID, func(_ context.Context, run *wire.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		replayHit = false
		if handle, ok, replayErr := replayUserInputFromVars(vars, run.ID, req.ToolCallID, inputDigest); replayErr != nil || ok {
			replayed, replayHit = handle, true
			return nil, false, replayErr
		}
		if scaffoldvars.HasPendingUserInput(vars) {
			return nil, false, askUserReject("ASK_USER_ALREADY_PENDING", pendingAskRejectData(vars))
		}
		vars = runstate.CloneAskVars(vars)
		if norm.rt.IsChoice() {
			vars = runstate.SetDecisionPending(vars, phaseID, norm.prompt, norm.options)
		} else {
			vars = runstate.SetFeedbackPending(vars, phaseID, norm.prompt)
		}
		return runstate.SetCoordinatorAsk(vars, runstate.CoordinatorAsk{
			ID: phaseID, RunID: run.ID, IssuedRevision: run.Revision + 1,
			Prompt: norm.prompt, ResponseType: norm.rt, Options: append([]string(nil), norm.options...),
			AllowOther: true, ArtifactID: norm.artifactID, ArtifactIDs: append([]string(nil), norm.artifactIDs...),
			Purpose: norm.purpose, ToolCallID: strings.TrimSpace(req.ToolCallID), InputDigest: inputDigest,
			Secret: norm.secret,
			State:  runstate.CoordinatorAskPending, CreatedAt: time.Now().UTC(),
		}), true, nil
	})
	if replayHit || err != nil || stamped == nil {
		attached.discard(ctx, m.Artifacts)
	}
	if replayHit {
		return replayed, err
	}
	if err != nil {
		return UserInputHandle{}, err
	}
	if stamped == nil {
		return UserInputHandle{}, askUserReject("ASK_USER_NO_ACTIVE_RUN", nil)
	}
	run = stamped

	handle := UserInputHandle{
		PhaseID:        phaseID,
		Prompt:         norm.prompt,
		ResponseType:   norm.rt,
		Purpose:        norm.purpose,
		RunID:          run.ID,
		IssuedRevision: run.Revision,
		Secret:         norm.secret,
	}
	unlockVars()
	varsUnlocked = true
	guard.Unlock()
	unlocked = true

	m.Feedback.NotifyPhasePending(ctx, sessionID, phaseID)
	if m.OnToolAskOpened != nil {
		m.OnToolAskOpened(ctx, sessionID, phaseID)
	}
	m.Publication.PublishSession(ctx, run)
	return handle, nil
}

// AnnouncePendingAsk appends one card after its tool row commits.
func (m *Asks) AnnouncePendingAsk(ctx context.Context, sessionID string) {
	if m == nil || m.Runs == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	guard := m.Guard(sessionID)
	guard.Lock()
	defer guard.Unlock()

	run, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil {
		return
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return
	}
	phaseID, fb, ok := pendingCoordinatorAskFromVars(vars)
	if !ok {
		return
	}
	_, card := runstate.BuildFeedbackAnnouncement(run, phaseID, fb, vars, runstate.AnnouncementMessageID(run.ID, phaseID))
	if card == nil {
		return
	}
	// The deterministic ID makes appends idempotent.
	msgs, err := m.Sessions.GetMessages(ctx, sessionID)
	if err != nil {
		return
	}
	for _, msg := range msgs {
		if msg.ID == card.ID {
			return
		}
	}
	m.Cards.AppendAnnouncement(ctx, sessionID, card)
}

// pendingCoordinatorAskFromVars rebuilds a pending ask card.

// attach records resolved artifact ids: one is a single review subject, two
// or more are the card's artifact set.

var askUserPurposes = []string{"clarify", "review", "compare"}

// validateAskPurpose checks that an explicit purpose names a mode and fits the
// artifact count: clarify carries none, review and compare carry some.

// attachedArtifacts are the resolved ids of one ask and the workspace copies
// it stored for them.
type attachedArtifacts struct {
	ids       []string
	projectID string
	copies    []string
}

// discard removes workspace copies of an ask that was not admitted.
func (a attachedArtifacts) discard(ctx context.Context, store visual.Store) {
	if store == nil {
		return
	}
	for _, id := range a.copies {
		_ = store.Discard(ctx, a.projectID, id)
	}
}

// resolveAskArtifacts resolves each ref as an artifact id or evidence handle
// in the session tree, and otherwise as a workspace image path.
func (m *Asks) resolveAskArtifacts(ctx context.Context, sessionID string, refs []string, workspace WorkspaceImageReader) (attachedArtifacts, error) {
	var out attachedArtifacts
	if len(refs) == 0 {
		return out, nil
	}
	if m == nil || m.Artifacts == nil {
		return out, askUserReject("ASK_USER_ARTIFACT_NOT_FOUND", map[string]any{"ref": refs[0], "reason": "store_unavailable"})
	}
	root := m.treeRoot(ctx, sessionID)
	for _, ref := range refs {
		id, err := m.resolveAskArtifact(ctx, sessionID, root, ref, workspace, &out)
		if err != nil {
			out.discard(ctx, m.Artifacts)
			return attachedArtifacts{}, err
		}
		out.ids = append(out.ids, id)
	}
	return out, nil
}

func (m *Asks) resolveAskArtifact(ctx context.Context, sessionID, root, ref string, workspace WorkspaceImageReader, out *attachedArtifacts) (string, error) {
	id, res := visual.ResolveRef(ctx, m.Artifacts, root, ref)
	switch {
	case res.IsPresent():
		if mime := res.Meta().Mime; !visual.IsInteractivePreviewMime(mime) {
			return "", askUserReject("ASK_USER_ARTIFACT_UNSUPPORTED", map[string]any{
				"ref": ref, "mime": mime, "ask_artifact_mime": mime, "ask_artifact_formats": visual.InteractivePreviewMIMEs(),
			})
		}
		return id, nil
	case res.Reason() == visual.AbsenceForeign:
		return "", askUserReject("ASK_USER_ARTIFACT_FOREIGN", map[string]any{"ref": ref})
	}
	if _, err := uuid.Parse(ref); err == nil || evidence.IsEvidenceHandleToken(ref) || workspace == nil {
		return "", askUserReject("ASK_USER_ARTIFACT_NOT_FOUND", map[string]any{"ref": ref, "reason": string(res.Reason())})
	}
	img, err := workspace(ctx, ref)
	if err != nil {
		return "", err
	}
	stored, err := m.Artifacts.Put(ctx, root, visual.Entry{
		Meta: wire.VisualArtifact{
			Mime:    img.Mime,
			Source:  wire.VisualArtifactSourceWorkspace,
			Caption: img.DisplayPath,
		},
		ProducerSessionID: sessionID,
		Bytes:             img.Bytes,
	})
	if err != nil {
		return "", err
	}
	out.projectID = m.sessionProjectID(ctx, sessionID)
	out.copies = append(out.copies, stored.ID)
	return stored.ID, nil
}

func (m *Asks) sessionProjectID(ctx context.Context, sessionID string) string {
	if m == nil || m.Sessions == nil {
		return ""
	}
	sess, err := m.Sessions.Get(ctx, strings.TrimSpace(sessionID))
	if err != nil || sess == nil {
		return ""
	}
	return sess.ProjectID
}

// Return only the field identity; protected values never enter recovery facts.

type Asks struct {
	Runs            runstate.RunsRepository
	Sessions        Sessions
	Vars            *runstate.Variables
	Publication     *publication.Runs
	Cards           *Cards
	Artifacts       visual.Store
	RootSessionID   func(context.Context, string) string
	SecretCapture   SecretCapture
	Approvals       AskApprovals
	Feedback        *Feedback
	Phases          PhaseProgress
	OnToolAskOpened func(context.Context, string, string)
	guards          sync.Map
}

func (m *Asks) Guard(sessionID string) *sync.Mutex {
	guard, _ := m.guards.LoadOrStore(sessionID, &sync.Mutex{})
	return guard.(*sync.Mutex)
}
func (m *Asks) ForgetSession(sessionID string) { m.guards.Delete(sessionID) }
func (m *Asks) treeRoot(ctx context.Context, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if m.RootSessionID != nil {
		if root := strings.TrimSpace(m.RootSessionID(ctx, sessionID)); root != "" {
			return root
		}
	}
	return sessionID
}
func (m *Asks) CancelPendingAsk(vars map[string]any, reason string) map[string]any {
	ask, ok := runstate.CoordinatorAskPendingFromVars(vars)
	if !ok {
		return nil
	}
	ask.State = runstate.CoordinatorAskCanceled
	ask.ClosedReason = reason
	return runstate.SetCoordinatorAsk(vars, ask)
}
