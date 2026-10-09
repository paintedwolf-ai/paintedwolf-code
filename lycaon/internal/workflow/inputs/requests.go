package inputs

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/promptresult"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/publication"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// AcceptsEmptyRequest reports whether an empty submit has defined workflow semantics.
func (m *Requests) AcceptsEmptyRequest(ctx context.Context, sessionID string) bool {
	if m == nil {
		return false
	}
	run, vars, err := m.Runs.ActiveStateBySession(ctx, sessionID)
	if err != nil {
		return false
	}
	if run == nil {
		return m.acceptsEmptyAmbientRequest(ctx, sessionID)
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil || manifest.Request == nil || runstate.RequestPending(vars) {
		return false
	}
	return acceptsEmptySubmit(manifest.Request, vars)
}

// Empty-submit preflight resolves the default without creating a run.
func (m *Requests) acceptsEmptyAmbientRequest(ctx context.Context, sessionID string) bool {
	sess, err := m.Sessions.Get(ctx, sessionID)
	if err != nil || sess == nil || sess.IsWorkerChild() {
		return false
	}
	ref, err := workflowdef.LoadRegistryConfig(extpacks.Bundled(config.PlatformFlows))
	if err != nil {
		return false
	}
	manifest, err := m.Resolver.ForSession(ctx, sess.WorkspacePath, sessionID, ref.ID, ref.Version)
	return err == nil && manifest.Request != nil && acceptsEmptySubmit(manifest.Request, nil)
}

// acceptsEmptySubmit reports whether this turn may resolve the request with no text.
func acceptsEmptySubmit(request *workflowdef.ManifestRequest, vars map[string]any) bool {
	state, _ := runstate.RequestStateFromVars(vars)
	return request.Cadence == workflowdef.RequestCadenceEachTurn || state == nil || state.Status == runstate.RequestStatusWaiting
}

// PrepareUserRequest resolves an active request before model execution.
func (m *Requests) PrepareUserRequest(ctx context.Context, sessionID, text string) (string, *promptresult.Result, bool, error) {
	text = strings.TrimSpace(text)
	if m == nil {
		return text, nil, false, nil
	}
	if _, err := m.Ambient.EnsureSessionWorkflow(ctx, sessionID); err != nil {
		return text, nil, false, err
	}
	run, vars, err := m.Runs.ActiveStateBySession(ctx, sessionID)
	if err != nil || run == nil {
		return text, nil, false, err
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil || manifest.Request == nil {
		return text, nil, false, err
	}
	if runstate.RequestPending(vars) {
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
	stamped, err := m.Vars.Stamp(ctx, run.ID, func(_ context.Context, current *api.WorkflowRun, currentVars map[string]any) (map[string]any, bool, error) {
		currentState, _ := runstate.RequestStateFromVars(currentVars)
		sequence := 1
		if currentState != nil {
			sequence = currentState.Sequence + 1
		}
		currentVars = runstate.SetRequestState(currentVars, manifest.Request, runstate.RequestStatusPending, "", "", sequence, true)
		currentVars = runstate.SetFeedbackPending(currentVars, runstate.WorkflowRequestFeedbackID, manifest.Request.Question)
		currentVars, card = runstate.StampPendingAnnouncement(current, currentVars)
		return currentVars, true, nil
	})
	if err != nil {
		return "", nil, false, err
	}
	m.Cards.AppendAnnouncement(ctx, sessionID, card)
	m.Feedback.NotifyPending(ctx, sessionID, map[string]any{"user_feedback": map[string]any{
		runstate.WorkflowRequestFeedbackID: map[string]any{"pending": true},
	}})
	messageID := run.ID
	if card != nil {
		messageID = card.ID
	}
	if stamped != nil {
		m.Publication.PublishSession(ctx, stamped)
	}
	return "", &promptresult.Result{
		MessageID: messageID,
	}, true, nil
}

func (m *Requests) stampResolvedRequest(ctx context.Context, runID string, request *workflowdef.ManifestRequest, text, source string) (*api.WorkflowRun, error) {
	return m.Vars.Stamp(ctx, runID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		state, _ := runstate.RequestStateFromVars(vars)
		sequence := 1
		if state != nil {
			sequence = state.Sequence + 1
		}
		return runstate.SetRequestState(vars, request, runstate.RequestStatusResolved, strings.TrimSpace(text), source, sequence, true), true, nil
	})
}

func (m *Requests) MarkRequestPhaseActive(ctx context.Context, runID string) (*api.WorkflowRun, error) {
	return m.Vars.Stamp(ctx, runID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		state, ok := runstate.RequestStateFromVars(vars)
		if !ok || runstate.RequestPhaseActive(vars) {
			return vars, false, nil
		}
		request := &workflowdef.ManifestRequest{Cadence: workflowdef.RequestCadence(state.Cadence)}
		return runstate.SetRequestState(vars, request, state.Status, state.Text, state.Source, state.Sequence, true), true, nil
	})
}

func (m *Requests) ResumeResolvedRequestPhase(ctx context.Context, run *api.WorkflowRun) (bool, error) {
	if m == nil || run == nil {
		return false, nil
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return false, err
	}
	state, ok := runstate.RequestStateFromVars(vars)
	if !ok || state.Status != runstate.RequestStatusResolved || runstate.RequestPhaseActive(vars) {
		return false, nil
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return false, err
	}
	sess, err := m.Sessions.Get(ctx, run.SessionID)
	if err != nil {
		return false, err
	}
	activated, err := m.Phases.ActivateInitial(ctx, run, manifest, sess.WorkspacePath, true)
	if err != nil {
		return false, err
	}
	activated, err = m.MarkRequestPhaseActive(ctx, activated.ID)
	if err != nil {
		return false, err
	}
	m.Publication.Publish(ctx, sess, activated)
	return true, nil
}

// NotifyAccepted runs only after a request has been committed.
func (m *Requests) NotifyAccepted(ctx context.Context, sessionID, text string) {
	if m.OnRequestAccepted != nil && strings.TrimSpace(text) != "" {
		m.OnRequestAccepted(ctx, sessionID, text)
	}
}

type Requests struct {
	Approvals         RequestApprovals
	Runs              runstate.RunsRepository
	Sessions          Sessions
	Resolver          *workflowcatalog.Resolver
	Vars              *runstate.Variables
	Ambient           Ambient
	Phases            RequestPhases
	Publication       *publication.Runs
	Feedback          *Feedback
	Cards             *Cards
	OnRequestAccepted func(context.Context, string, string)
}

func (m *Requests) ResolveWorkflowRequest(ctx context.Context, answererID, sessionID string, run *api.WorkflowRun, manifest workflowdef.Manifest, response string) (*api.WorkflowRun, error) {
	phaseWasActive := false
	var card *api.Message
	var resolvedVars map[string]any
	stamped, err := m.Vars.Stamp(ctx, run.ID, func(ctx context.Context, current *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		card = nil
		resolvedVars = nil
		if !runstate.FeedbackPending(vars, runstate.WorkflowRequestFeedbackID) || !runstate.RequestPending(vars) {
			return nil, false, runstate.ErrFeedbackNotPending
		}
		state, _ := runstate.RequestStateFromVars(vars)
		sequence := 1
		if state != nil && state.Sequence > 0 {
			sequence = state.Sequence
		}
		phaseWasActive = runstate.RequestPhaseActive(vars)
		vars = runstate.SetFeedbackResponse(vars, runstate.WorkflowRequestFeedbackID, response)
		vars = runstate.SetRequestState(vars, manifest.Request, runstate.RequestStatusResolved, response, "answer", sequence, phaseWasActive)
		if !phaseWasActive {
			var err error
			sess, err := m.Sessions.Get(ctx, sessionID)
			if err != nil {
				return nil, false, err
			}
			vars, err = m.Phases.InitializeVars(ctx, sess, current, manifest, vars)
			if err != nil {
				return nil, false, err
			}
			if def, ok := manifest.PhaseByID(current.CurrentPhase); ok {
				vars, err = m.Approvals.AutoApproveOnPhase(ctx, current, manifest, def, vars)
				if err != nil {
					return nil, false, err
				}
			}
			vars, card = runstate.StampPendingAnnouncement(current, vars)
		}
		resolvedVars = vars
		return vars, true, nil
	})
	if err != nil {
		return nil, err
	}
	m.NotifyAccepted(ctx, sessionID, response)
	m.Cards.StampFeedbackAnswer(ctx, sessionID, run.ID, runstate.WorkflowRequestFeedbackID, response, answererID)
	m.Feedback.NotifyResolved(ctx, sessionID, run.ID, runstate.WorkflowRequestFeedbackID, response)
	m.Cards.AppendAnnouncement(ctx, sessionID, card)
	m.Feedback.NotifyPending(ctx, sessionID, resolvedVars)
	if phaseWasActive {
		m.Publication.PublishSession(ctx, stamped)
		return stamped, nil
	}
	sess, err := m.Sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	activated, err := m.Phases.ActivateInitial(ctx, stamped, manifest, sess.WorkspacePath, true)
	if err != nil {
		return nil, err
	}
	activated, err = m.MarkRequestPhaseActive(ctx, activated.ID)
	if err != nil {
		return nil, err
	}
	m.Publication.Publish(ctx, sess, activated)
	return activated, nil
}
