package workflow

import (
	"context"
	"github.com/lycaon/lycaon/internal/promptresult"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

const workflowRequestFeedbackID = "workflow_request"

const (
	requestStatusWaiting  = "waiting"
	requestStatusPending  = "pending"
	requestStatusResolved = "resolved"
)

func initializeRequestVars(vars map[string]any, request *workflowdef.ManifestRequest, explicit string, activationOnly bool) map[string]any {
	if request == nil {
		return vars
	}
	vars = cloneVars(vars)
	if activationOnly {
		return setRequestState(vars, request, requestStatusWaiting, "", "", 0, true)
	}
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return setRequestState(vars, request, requestStatusResolved, explicit, "explicit", 1, true)
	}
	if fallback := strings.TrimSpace(request.Default); fallback != "" {
		return setRequestState(vars, request, requestStatusResolved, fallback, "default", 1, true)
	}
	vars = setRequestState(vars, request, requestStatusPending, "", "", 1, false)
	return setFeedbackPending(vars, workflowRequestFeedbackID, request.Question)
}

func setRequestState(vars map[string]any, request *workflowdef.ManifestRequest, status, text, source string, sequence int, phaseActive bool) map[string]any {
	vars = cloneVars(vars)
	state := map[string]any{
		"cadence":      string(request.Cadence),
		"status":       status,
		"sequence":     sequence,
		"phase_active": phaseActive,
	}
	if text != "" {
		state["text"] = text
	}
	if source != "" {
		state["source"] = source
	}
	vars[workflowRequestFeedbackID] = state
	return vars
}

func requestStateFromVars(vars map[string]any) (*api.WorkflowRequestState, bool) {
	raw, ok := vars[workflowRequestFeedbackID].(map[string]any)
	if !ok {
		return nil, false
	}
	state := &api.WorkflowRequestState{}
	state.Cadence, _ = raw["cadence"].(string)
	state.Status, _ = raw["status"].(string)
	state.Text, _ = raw["text"].(string)
	state.Source, _ = raw["source"].(string)
	switch sequence := raw["sequence"].(type) {
	case int:
		state.Sequence = sequence
	case int64:
		state.Sequence = int(sequence)
	case float64:
		state.Sequence = int(sequence)
	}
	return state, strings.TrimSpace(state.Cadence) != "" && strings.TrimSpace(state.Status) != ""
}

func requestPending(vars map[string]any) bool {
	state, ok := requestStateFromVars(vars)
	return ok && state.Status == requestStatusPending
}

func requestPhaseActive(vars map[string]any) bool {
	raw, _ := vars[workflowRequestFeedbackID].(map[string]any)
	active, _ := raw["phase_active"].(bool)
	return active
}

// AcceptsEmptyRequest reports whether an empty submit has defined workflow semantics.
func (m *RunManager) AcceptsEmptyRequest(ctx context.Context, sessionID string) bool {
	if m == nil {
		return false
	}
	run, vars, err := m.Store.ActiveStateBySession(ctx, sessionID)
	if err != nil {
		return false
	}
	if run == nil {
		return m.acceptsEmptyAmbientRequest(ctx, sessionID)
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil || manifest.Request == nil || requestPending(vars) {
		return false
	}
	return acceptsEmptySubmit(manifest.Request, vars)
}

// Empty-submit preflight resolves the default without creating a run.
func (m *RunManager) acceptsEmptyAmbientRequest(ctx context.Context, sessionID string) bool {
	sess, err := m.Sessions.Get(ctx, sessionID)
	if err != nil || sess == nil || sess.IsWorkerChild() {
		return false
	}
	ref, err := workflowdef.LoadRegistryConfig(extpacks.Bundled(config.PlatformFlows))
	if err != nil {
		return false
	}
	manifest, err := m.manifestForSession(ctx, sess.WorkspacePath, sessionID, ref.ID, ref.Version)
	return err == nil && manifest.Request != nil && acceptsEmptySubmit(manifest.Request, nil)
}

// acceptsEmptySubmit reports whether this turn may resolve the request with no text.
func acceptsEmptySubmit(request *workflowdef.ManifestRequest, vars map[string]any) bool {
	state, _ := requestStateFromVars(vars)
	return request.Cadence == workflowdef.RequestCadenceEachTurn || state == nil || state.Status == requestStatusWaiting
}

// PrepareUserRequest resolves an active request before model execution.
func (m *RunManager) PrepareUserRequest(ctx context.Context, sessionID, text string) (string, *promptresult.Result, bool, error) {
	text = strings.TrimSpace(text)
	if m == nil {
		return text, nil, false, nil
	}
	if _, err := m.EnsureSessionWorkflow(ctx, sessionID); err != nil {
		return text, nil, false, err
	}
	run, vars, err := m.Store.ActiveStateBySession(ctx, sessionID)
	if err != nil || run == nil {
		return text, nil, false, err
	}
	manifest, err := m.runnableManifestForRun(ctx, run)
	if err != nil || manifest.Request == nil {
		return text, nil, false, err
	}
	if requestPending(vars) {
		return text, nil, false, nil
	}
	if !acceptsEmptySubmit(manifest.Request, vars) {
		return text, nil, false, nil
	}
	if text != "" {
		_, err = m.stampResolvedRequest(ctx, run.ID, manifest.Request, text, "explicit")
		return text, nil, false, err
	}
	if fallback := strings.TrimSpace(manifest.Request.Default); fallback != "" {
		_, err = m.stampResolvedRequest(ctx, run.ID, manifest.Request, fallback, "default")
		return fallback, nil, false, err
	}
	var card *api.Message
	stamped, err := m.StampRunVars(ctx, run.ID, func(_ context.Context, current *api.WorkflowRun, currentVars map[string]any) (map[string]any, bool, error) {
		currentState, _ := requestStateFromVars(currentVars)
		sequence := 1
		if currentState != nil {
			sequence = currentState.Sequence + 1
		}
		currentVars = setRequestState(currentVars, manifest.Request, requestStatusPending, "", "", sequence, true)
		currentVars = setFeedbackPending(currentVars, workflowRequestFeedbackID, manifest.Request.Question)
		currentVars, card = stampPendingAnnouncement(current, currentVars)
		return currentVars, true, nil
	})
	if err != nil {
		return "", nil, false, err
	}
	m.appendAnnouncement(ctx, sessionID, card)
	m.notifyFeedbackPending(ctx, sessionID, map[string]any{"user_feedback": map[string]any{
		workflowRequestFeedbackID: map[string]any{"pending": true},
	}})
	messageID := run.ID
	if card != nil {
		messageID = card.ID
	}
	if stamped != nil {
		m.publishSession(ctx, stamped)
	}
	return "", &promptresult.Result{
		MessageID: messageID,
	}, true, nil
}

func (m *RunManager) stampResolvedRequest(ctx context.Context, runID string, request *workflowdef.ManifestRequest, text, source string) (*api.WorkflowRun, error) {
	return m.StampRunVars(ctx, runID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		state, _ := requestStateFromVars(vars)
		sequence := 1
		if state != nil {
			sequence = state.Sequence + 1
		}
		return setRequestState(vars, request, requestStatusResolved, strings.TrimSpace(text), source, sequence, true), true, nil
	})
}

func (m *RunManager) markRequestPhaseActive(ctx context.Context, runID string) (*api.WorkflowRun, error) {
	return m.StampRunVars(ctx, runID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		state, ok := requestStateFromVars(vars)
		if !ok || requestPhaseActive(vars) {
			return vars, false, nil
		}
		request := &workflowdef.ManifestRequest{Cadence: workflowdef.RequestCadence(state.Cadence)}
		return setRequestState(vars, request, state.Status, state.Text, state.Source, state.Sequence, true), true, nil
	})
}

func (m *RunManager) resumeResolvedRequestPhase(ctx context.Context, run *api.WorkflowRun) (bool, error) {
	if m == nil || run == nil {
		return false, nil
	}
	vars, err := m.Store.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return false, err
	}
	state, ok := requestStateFromVars(vars)
	if !ok || state.Status != requestStatusResolved || requestPhaseActive(vars) {
		return false, nil
	}
	manifest, err := m.runnableManifestForRun(ctx, run)
	if err != nil {
		return false, err
	}
	sess, err := m.Sessions.Get(ctx, run.SessionID)
	if err != nil {
		return false, err
	}
	activated, err := m.activateInitialWorkflowPhase(ctx, run, manifest, sess.WorkspacePath, true)
	if err != nil {
		return false, err
	}
	activated, err = m.markRequestPhaseActive(ctx, activated.ID)
	if err != nil {
		return false, err
	}
	m.publish(ctx, sess, activated)
	return true, nil
}

// notifyRequestAccepted runs only after a request has been committed.
func (m *RunManager) notifyRequestAccepted(ctx context.Context, sessionID, text string) {
	if m.OnRequestAccepted != nil && strings.TrimSpace(text) != "" {
		m.OnRequestAccepted(ctx, sessionID, text)
	}
}
