package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/visual"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
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
func (m *RunManager) RequestUserInput(ctx context.Context, sessionID string, req UserInputRequest) (UserInputHandle, error) {
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
	guard := m.askUserGuardFor(sessionID)
	guard.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			guard.Unlock()
		}
	}()

	run, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil {
		return UserInputHandle{}, err
	}
	if run == nil {
		return UserInputHandle{}, askUserReject("ASK_USER_NO_ACTIVE_RUN", nil)
	}
	if approval := m.inheritedBlueprintApprovalSnapshot(ctx, run); approval != nil {
		return UserInputHandle{}, askUserReject("ASK_USER_INHERITED_BLUEPRINT_FORBIDDEN", map[string]any{
			"blueprint_status":           approval.Status,
			"inherited_blueprint_status": approval.Status, "inherited_blueprint_approved": approval.Status == "approved",
			"inherited_parent_run": approval.ParentRunID,
			"parent_run_id":        approval.ParentRunID,
		})
	}

	unlockVars := m.lockRunVars(run.ID)
	varsUnlocked := false
	defer func() {
		if !varsUnlocked {
			unlockVars()
		}
	}()
	// Admission checks run before any artifact is copied, so a refused or
	// replayed ask leaves no stored image behind.
	vars, err := m.Store.GetScaffoldVars(ctx, run.ID)
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
	stamped, err := m.StampRunVarsLocked(ctx, run.ID, func(_ context.Context, run *wire.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		replayHit = false
		if handle, ok, replayErr := replayUserInputFromVars(vars, run.ID, req.ToolCallID, inputDigest); replayErr != nil || ok {
			replayed, replayHit = handle, true
			return nil, false, replayErr
		}
		if scaffoldvars.HasPendingUserInput(vars) {
			return nil, false, askUserReject("ASK_USER_ALREADY_PENDING", pendingAskRejectData(vars))
		}
		vars = cloneAskVars(vars)
		if norm.rt.IsChoice() {
			vars = setDecisionPending(vars, phaseID, norm.prompt, norm.options)
		} else {
			vars = setFeedbackPending(vars, phaseID, norm.prompt)
		}
		return setCoordinatorAsk(vars, coordinatorAsk{
			ID: phaseID, RunID: run.ID, IssuedRevision: run.Revision + 1,
			Prompt: norm.prompt, ResponseType: norm.rt, Options: append([]string(nil), norm.options...),
			AllowOther: true, ArtifactID: norm.artifactID, ArtifactIDs: append([]string(nil), norm.artifactIDs...),
			Purpose: norm.purpose, ToolCallID: strings.TrimSpace(req.ToolCallID), InputDigest: inputDigest,
			Secret: norm.secret,
			State:  coordinatorAskPending, CreatedAt: time.Now().UTC(),
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

	if m.OnFeedbackPending != nil {
		m.OnFeedbackPending(ctx, sessionID, phaseID)
	}
	if m.OnToolAskOpened != nil {
		m.OnToolAskOpened(ctx, sessionID, phaseID)
	}
	m.publishSession(ctx, run)
	return handle, nil
}

func askUserInputDigest(norm normalizedAskUser) (string, error) {
	return workflowCommandDigest("ask_user", struct {
		Prompt    string                           `json:"prompt"`
		Purpose   string                           `json:"purpose"`
		Type      workflowdef.FeedbackResponseType `json:"response_type"`
		Options   []string                         `json:"options"`
		Artifacts []string                         `json:"artifacts"`
		Secret    *workflowdef.SecretInputSpec     `json:"secret,omitempty"`
	}{
		Prompt: norm.prompt, Purpose: norm.purpose, Type: norm.rt,
		Options: append([]string(nil), norm.options...), Artifacts: append([]string(nil), norm.artifactRefs...), Secret: norm.secret,
	})
}

func replayUserInputFromVars(
	vars map[string]any,
	runID, toolCallID, inputDigest string,
) (UserInputHandle, bool, error) {
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return UserInputHandle{}, false, nil
	}
	ask, ok := coordinatorAskFromVars(vars)
	if !ok || ask.ToolCallID != toolCallID {
		return UserInputHandle{}, false, nil
	}
	if ask.InputDigest == "" || ask.InputDigest != inputDigest {
		return UserInputHandle{}, false, askUserReject("ASK_USER_OPERATION_CONFLICT", map[string]any{"tool_call_id": toolCallID})
	}
	return UserInputHandle{PhaseID: ask.ID, Prompt: ask.Prompt, ResponseType: ask.ResponseType, Purpose: ask.Purpose, RunID: runID, IssuedRevision: ask.IssuedRevision, Secret: ask.Secret}, true, nil
}

// AnnouncePendingAsk appends one card after its tool row commits.
func (m *RunManager) AnnouncePendingAsk(ctx context.Context, sessionID string) {
	if m == nil || m.Store == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	guard := m.askUserGuardFor(sessionID)
	guard.Lock()
	defer guard.Unlock()

	run, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil {
		return
	}
	vars, err := m.Store.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return
	}
	phaseID, fb, ok := pendingCoordinatorAskFromVars(vars)
	if !ok {
		return
	}
	_, card := buildFeedbackAnnouncement(run, phaseID, fb, vars, workflowAnnouncementMessageID(run.ID, phaseID))
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
	m.appendAnnouncement(ctx, sessionID, card)
}

// pendingCoordinatorAskFromVars rebuilds a pending ask card.
func pendingCoordinatorAskFromVars(vars map[string]any) (string, *workflowdef.UserFeedbackPrompt, bool) {
	if ask, ok := coordinatorAskPendingFromVars(vars); ok {
		return ask.ID, &workflowdef.UserFeedbackPrompt{
			Prompt: ask.Prompt, ResponseType: ask.ResponseType, Options: append([]string(nil), ask.Options...),
			AllowOther: ask.AllowOther, ArtifactID: ask.ArtifactID, ArtifactIDs: append([]string(nil), ask.ArtifactIDs...), Purpose: ask.Purpose,
			Secret: ask.Secret,
		}, true
	}
	return "", nil, false
}

type normalizedAskUser struct {
	prompt       string
	purpose      string
	rt           workflowdef.FeedbackResponseType
	options      []string
	artifactID   string
	artifactIDs  []string
	artifactRefs []string
	secret       *workflowdef.SecretInputSpec
}

// attach records resolved artifact ids: one is a single review subject, two
// or more are the card's artifact set.
func (n *normalizedAskUser) attach(ids []string) {
	switch {
	case len(ids) == 1:
		n.artifactID, n.artifactIDs = ids[0], nil
	case len(ids) >= askUserCompareMin:
		n.artifactID, n.artifactIDs = "", append([]string(nil), ids...)
	}
}

func normalizeAskUserRequest(req UserInputRequest) (normalizedAskUser, error) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return normalizedAskUser{}, askUserReject("ASK_USER_PROMPT_REQUIRED", nil)
	}
	if n := utf8.RuneCountInString(prompt); n > MaxAskUserPromptRunes {
		return normalizedAskUser{}, askUserReject("ASK_USER_PROMPT_TOO_LONG", map[string]any{
			"max_runes":        MaxAskUserPromptRunes,
			"ask_prompt_runes": n, "ask_prompt_limit": MaxAskUserPromptRunes,
			"runes": n,
		})
	}

	refs := cleanAskOptions(req.Artifacts)
	n := len(refs)
	if n > askUserCompareMax {
		return normalizedAskUser{}, askUserReject("ASK_USER_COMPARE_ARITY", map[string]any{"count": n, "ask_artifact_count": n, "ask_artifact_limit": askUserCompareMax})
	}

	rawRT := req.ResponseType
	switch rawRT {
	case "", workflowdef.FeedbackResponseText, workflowdef.FeedbackResponseSingleChoice, workflowdef.FeedbackResponseMultiChoice, workflowdef.FeedbackResponseSecret:
	default:
		return normalizedAskUser{}, askUserReject("ASK_USER_RESPONSE_TYPE_INVALID", map[string]any{"response_type": string(rawRT)})
	}
	if rawRT == workflowdef.FeedbackResponseSecret && n > 0 {
		return normalizedAskUser{}, askUserReject("ASK_USER_SECRET_ARTIFACTS_FORBIDDEN", map[string]any{"count": n})
	}
	if rawRT != workflowdef.FeedbackResponseSecret && req.Secret != nil {
		return normalizedAskUser{}, askUserReject("ASK_USER_SECRET_METADATA_FORBIDDEN", map[string]any{"response_type": string(rawRT)})
	}
	reqPurpose := strings.ToLower(strings.TrimSpace(req.Purpose))
	if err := validateAskPurpose(req.Purpose, reqPurpose, n); err != nil {
		return normalizedAskUser{}, err
	}

	options := cleanAskOptions(req.Options)
	purpose := "clarify"
	var rt workflowdef.FeedbackResponseType

	switch n {
	case 0:
		rt = rawRT
		if rt == "" {
			rt = workflowdef.FeedbackResponseText
		}
		if reqPurpose == "compare" {
			return normalizedAskUser{}, askUserReject("ASK_USER_COMPARE_ARITY", map[string]any{"count": 0, "ask_artifact_count": 0, "ask_artifact_limit": askUserCompareMax})
		}
		if rt == workflowdef.FeedbackResponseSecret {
			if len(options) > 0 {
				return normalizedAskUser{}, askUserReject("ASK_USER_OPTIONS_FORBIDDEN", map[string]any{"response_type": string(rt)})
			}
			if req.Secret == nil || strings.TrimSpace(req.Secret.Name) == "" {
				return normalizedAskUser{}, askUserReject("ASK_USER_SECRET_METADATA_REQUIRED", map[string]any{"field": "secret.name"})
			}
			secret := *req.Secret
			secret.Name = strings.TrimSpace(secret.Name)
			secret.Purpose = strings.TrimSpace(secret.Purpose)
			secret.Scope = strings.TrimSpace(secret.Scope)
			if secret.Scope == "" {
				secret.Scope = "chat"
			}
			if field := invalidSecretMetadataField(secret); field != "" {
				return normalizedAskUser{}, askUserReject("ASK_USER_SECRET_METADATA_INVALID", map[string]any{"field": field})
			}
			if secret.Scope == "project" && secret.Purpose == "" {
				return normalizedAskUser{}, askUserReject("ASK_USER_SECRET_METADATA_REQUIRED", map[string]any{
					"field": "secret.purpose",
				})
			}
			req.Secret = &secret
			purpose = "secret"
		} else if rt.IsChoice() {
			if len(options) < 2 {
				return normalizedAskUser{}, askUserReject("ASK_USER_OPTIONS_REQUIRED", map[string]any{"ask_option_count": len(options), "ask_option_limit": 2})
			}
			if len(options) > MaxAskUserOptions {
				return normalizedAskUser{}, askUserReject("ASK_USER_OPTIONS_TOO_MANY", map[string]any{
					"max_options":      MaxAskUserOptions,
					"ask_option_count": len(options), "ask_option_limit": MaxAskUserOptions,
					"count": len(options),
				})
			}
		} else if len(options) > 0 {
			return normalizedAskUser{}, askUserReject("ASK_USER_OPTIONS_FORBIDDEN", map[string]any{"response_type": string(rt)})
		}
	default:
		// Artifacts default to review; compare is an explicit purpose.
		if reqPurpose == "compare" {
			if n < askUserCompareMin {
				return normalizedAskUser{}, askUserReject("ASK_USER_COMPARE_ARITY", map[string]any{"count": n, "ask_artifact_count": n, "ask_artifact_limit": askUserCompareMax})
			}
			purpose = "compare"
			if len(options) > 0 {
				return normalizedAskUser{}, askUserReject("ASK_USER_COMPARE_OPTIONS", map[string]any{
					"detail": "host synthesizes compare options",
					"count":  n,
				})
			}
			if rawRT != "" && rawRT != workflowdef.FeedbackResponseSingleChoice {
				return normalizedAskUser{}, askUserReject("ASK_USER_COMPARE_OPTIONS", map[string]any{
					"detail": "compare requires single_choice",
					"count":  n,
				})
			}
			rt = workflowdef.FeedbackResponseSingleChoice
			options = synthesizeCompareOptions(n)
		} else {
			purpose = "review"
			if rawRT == workflowdef.FeedbackResponseMultiChoice {
				return normalizedAskUser{}, askUserReject("ASK_USER_REVIEW_OPTIONS", map[string]any{
					"detail": "review does not support multi_choice",
					"count":  n,
				})
			}
			if rawRT == workflowdef.FeedbackResponseText {
				if len(options) > 0 {
					return normalizedAskUser{}, askUserReject("ASK_USER_OPTIONS_FORBIDDEN", map[string]any{"response_type": string(rawRT)})
				}
				rt = workflowdef.FeedbackResponseText
				options = nil
			} else {
				rt = workflowdef.FeedbackResponseSingleChoice
				if len(options) > 0 {
					return normalizedAskUser{}, askUserReject("ASK_USER_REVIEW_OPTIONS", map[string]any{
						"detail": "host synthesizes review options",
						"count":  n,
					})
				}
				options = append([]string(nil), askUserReviewOptions...)
			}
		}
	}

	return normalizedAskUser{
		prompt:       prompt,
		purpose:      purpose,
		rt:           rt,
		options:      options,
		artifactRefs: refs,
		secret:       req.Secret,
	}, nil
}

var askUserPurposes = []string{"clarify", "review", "compare"}

// validateAskPurpose checks that an explicit purpose names a mode and fits the
// artifact count: clarify carries none, review and compare carry some.
func validateAskPurpose(raw, purpose string, artifacts int) error {
	fits := true
	switch purpose {
	case "":
	case "clarify":
		fits = artifacts == 0
	case "review":
		fits = artifacts > 0
	case "compare":
		// Arity is reported by ASK_USER_COMPARE_ARITY.
	default:
		fits = false
	}
	if fits {
		return nil
	}
	return askUserReject("ASK_USER_PURPOSE_INVALID", map[string]any{
		"purpose":            raw,
		"ask_purpose":        strings.TrimSpace(raw),
		"ask_purposes":       append([]string(nil), askUserPurposes...),
		"ask_artifact_count": artifacts,
	})
}

func pendingAskRejectData(vars map[string]any) map[string]any {
	if ask, ok := coordinatorAskPendingFromVars(vars); ok {
		return map[string]any{"phase_id": ask.ID, "pending_input_id": ask.ID, "prompt": ask.Prompt}
	}
	phaseID, ok := pendingFeedbackPhase(vars)
	if !ok {
		phaseID = firstPendingDecisionPhase(vars)
	}
	phaseID = strings.TrimSpace(phaseID)
	if phaseID == "" {
		return nil
	}
	data := map[string]any{"phase_id": phaseID, "pending_input_id": phaseID}
	return data
}

func firstPendingDecisionPhase(vars map[string]any) string {
	bucket, _ := vars["user_decision"].(map[string]any)
	if bucket == nil {
		return ""
	}
	for phaseID, raw := range bucket {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if pending, _ := entry["pending"].(bool); pending {
			return strings.TrimSpace(phaseID)
		}
	}
	return ""
}

func cleanAskOptions(options []string) []string {
	out := make([]string, 0, len(options))
	seen := make(map[string]struct{}, len(options))
	for _, o := range options {
		if o = strings.TrimSpace(o); o != "" {
			if _, exists := seen[o]; exists {
				continue
			}
			seen[o] = struct{}{}
			out = append(out, o)
		}
	}
	return out
}

func synthesizeCompareOptions(n int) []string {
	if n < askUserCompareMin {
		n = askUserCompareMin
	}
	if n > askUserCompareMax {
		n = askUserCompareMax
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, askUserCompareLabels[i])
	}
	return out
}

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
func (m *RunManager) resolveAskArtifacts(ctx context.Context, sessionID string, refs []string, workspace WorkspaceImageReader) (attachedArtifacts, error) {
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

func (m *RunManager) resolveAskArtifact(ctx context.Context, sessionID, root, ref string, workspace WorkspaceImageReader, out *attachedArtifacts) (string, error) {
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

func (m *RunManager) sessionProjectID(ctx context.Context, sessionID string) string {
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
func invalidSecretMetadataField(secret workflowdef.SecretInputSpec) string {
	switch {
	case utf8.RuneCountInString(secret.Name) > 80:
		return "secret.name"
	case utf8.RuneCountInString(secret.Purpose) > 240:
		return "secret.purpose"
	case secret.Scope != "chat" && secret.Scope != "project":
		return "secret.scope"
	case secret.AgentUseTTLSeconds < 0 || secret.AgentUseTTLSeconds > maxAskSecretAgentUseLifetimeSeconds:
		return "secret.agent_use_ttl_seconds"
	default:
		return ""
	}
}
