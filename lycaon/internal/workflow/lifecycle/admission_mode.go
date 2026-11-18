package lifecycle

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/session/store"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type sessionModeAdmission interface {
	WithModeAdmission(context.Context, string, func(context.Context) error) error
}

func (m *Admission) withinModeAdmission(ctx context.Context, id string, fn func(context.Context) error) error {
	sessions, ok := m.Sessions.(sessionModeAdmission)
	if !ok {
		return store.ErrSessionModeUnavailable
	}
	return sessions.WithModeAdmission(ctx, id, fn)
}

func (m *Admission) startMode(ctx context.Context, id string, intent *api.SessionMode, requiresAgent bool) (api.SessionMode, error) {
	authority, err := m.Sessions.ResolveEffectiveMode(ctx, id)
	if err != nil {
		return "", err
	}
	mode, err := authority.WithModeIntent(intent)
	if err != nil {
		return "", err
	}
	return mode, runstate.ValidateWorkflowMode(mode, requiresAgent)
}

type committedWorkflowStart struct {
	Session  *api.Session
	Manifest workflowdef.Manifest
	Run      *api.WorkflowRun
	Vars     map[string]any
	Replaced []api.WorkflowRun
}

func (m *Admission) commitStart(ctx context.Context, sessionID string, req api.StartWorkflowRunRequest, slashText, startDigest string, humanStart, ambientStart bool) (*committedWorkflowStart, error) {
	explicitReplacement := strings.TrimSpace(req.ReplaceRunID) != ""
	workflowID := strings.TrimSpace(req.WorkflowID)
	version := strings.TrimSpace(req.WorkflowVersion)
	presetID := strings.TrimSpace(req.PresetID)
	if workflowID == "" || version == "" {
		return nil, workflowdef.ErrUnknownWorkflow
	}
	sess, err := m.Sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if ambientStart && active != nil {
		return nil, runstate.ErrActiveRunExists
	}
	automaticReplacement, _ := ctx.Value(automaticHumanReplacementKey{}).(bool)
	if automaticReplacement && strings.TrimSpace(req.ReplaceRunID) == "" && req.ExpectedRevision == 0 && active != nil {
		req.ReplaceRunID = active.ID
		req.ExpectedRevision = active.Revision
	}
	manifest, err := m.Resolver.ForSession(ctx, sess.WorkspacePath, sessionID, workflowID, version)
	if err != nil {
		return nil, err
	}
	if manifest.Retired {
		return nil, workflowdef.ErrUnknownWorkflow
	}
	requiresAgent := manifest.EntryRequiresAgent()
	mode, err := m.startMode(ctx, sessionID, req.Mode, requiresAgent && !ambientStart)
	if err != nil {
		return nil, err
	}
	if ambientStart && mode == api.SessionModeAsk {
		return nil, nil
	}
	if err := m.validateActiveRunForStart(active, runstate.IsAmbientRun(active), manifest, humanStart); err != nil {
		return nil, err
	}
	replacementTarget, err := m.reviewedReplacementTarget(ctx, sessionID, active, req, humanStart)
	if err != nil {
		return nil, err
	}
	initialized, err := m.initializeWorkflowStart(ctx, sess, active, manifest, req, slashText, presetID, workflowID, version, ambientStart)
	if err != nil {
		return nil, err
	}
	run := initialized.Run
	initialized.Mutation.BindSessionMode = humanStart
	initialized.Mutation.ModeIntent = req.Mode
	initialized.Mutation.RequiresAgent = requiresAgent
	initialized.Mutation.AllowReplacementRebase = replacementTarget != nil && !explicitReplacement
	if replacementTarget != nil {
		initialized.Mutation.ReplacementTeardowns, err = m.Cleanup.ForSession(ctx, sessionID, runstate.ExitReasonSupersededByWorkflowStart)
		if err != nil {
			return nil, err
		}
	}
	replaced, err := m.Starts.ActivateStart(ctx, req.OperationID, startDigest, replacementTarget, run, initialized.Mutation)
	if err != nil {
		return nil, err
	}
	return &committedWorkflowStart{Session: sess, Manifest: manifest, Run: run, Vars: initialized.Vars, Replaced: replaced}, nil
}
