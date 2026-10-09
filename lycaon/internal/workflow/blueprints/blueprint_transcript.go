package blueprints

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func buildBlueprintTranscriptMeta(
	run *api.WorkflowRun,
	blueprint *api.Blueprint,
	awaitingApproval, changedWarning bool,
) api.BlueprintMeta {
	phase := ""
	if run != nil {
		phase = strings.TrimSpace(run.CurrentPhase)
	}
	meta := api.BlueprintMeta{
		BlueprintPath:  blueprint.Path,
		Revision:       blueprint.Version,
		RevisionKey:    blueprint.UpdatedAt.UTC().Format(time.RFC3339Nano),
		BlueprintTitle: strings.TrimSpace(blueprint.Title),
	}
	if meta.BlueprintTitle == "" {
		meta.BlueprintTitle = "Blueprint"
	}

	// PauseReason distinguishes rejection from a superseded draft.
	ended := run != nil && runstate.IsTerminal(run.Status) && blueprint.Status == api.BlueprintStatusDraft
	superseded := ended && strings.TrimSpace(run.PauseReason) == runstate.ExitReasonSupersededByWorkflowStart

	switch {
	case blueprint.Status == api.BlueprintStatusApproved ||
		blueprint.Status == api.BlueprintStatusImplementing ||
		blueprint.Status == api.BlueprintStatusDone:
		meta.Status = api.BlueprintTranscriptStatusApproved
	case superseded:
		meta.Status = api.BlueprintTranscriptStatusSuperseded
	case ended:
		meta.Status = api.BlueprintTranscriptStatusRejected
	case blueprint.Status == api.BlueprintStatusDraft && awaitingApproval && changedWarning:
		meta.Status = api.BlueprintTranscriptStatusRevised
	case blueprint.Status == api.BlueprintStatusDraft && awaitingApproval:
		meta.Status = api.BlueprintTranscriptStatusAwaitingApproval
	default:
		meta.Status = api.BlueprintTranscriptStatusProposed
	}

	meta.Phase = api.BlueprintCardPhaseDrafting
	meta.PhaseLabel = blueprintPhaseLabel(phase)
	switch {
	case ended:
		meta.Phase = api.BlueprintCardPhaseRejected
		meta.PhaseLabel = "Rejected"
		if superseded {
			meta.PhaseLabel = "Superseded"
		}
		meta.Collapsed = true
	case phase == "execute":
		meta.Phase = api.BlueprintCardPhaseBuilding
		meta.PhaseLabel = "Building…"
		meta.Collapsed = true
	case phase == "done":
		meta.Phase = api.BlueprintCardPhaseApproved
		meta.PhaseLabel = "Done"
		meta.Collapsed = true
	case awaitingApproval:
		if changedWarning {
			meta.Phase = api.BlueprintCardPhaseChanged
			meta.PhaseLabel = "Blueprint changed — approve again"
		} else {
			meta.Phase = api.BlueprintCardPhaseReady
			meta.PhaseLabel = "Ready for review"
		}
	case phase == "expand" || phase == "stub":
		meta.PhaseLabel = "Writing the blueprint"
	}

	meta.CanApprove = awaitingApproval && !ended
	meta.ShowActions = meta.CanApprove && !meta.Collapsed
	return meta
}

func blueprintPhaseLabel(phase string) string {
	switch strings.TrimSpace(phase) {
	case "intake":
		return "Intake"
	case "research":
		return "Research"
	case "expand":
		return "Writing the blueprint"
	case "review":
		return "Review"
	case "approve":
		return "Approve"
	case "execute":
		return "Building…"
	case "done":
		return "Done"
	default:
		if phase == "" {
			return "Blueprint"
		}
		return phase
	}
}

func findBlueprintTranscriptMessage(msgs []api.Message, blueprintPath string) (api.Message, bool) {
	blueprintPath = strings.TrimSpace(blueprintPath)
	for i := len(msgs) - 1; i >= 0; i-- {
		if !api.IsBlueprintMessage(msgs[i]) {
			continue
		}
		if msgs[i].Blueprint != nil && strings.TrimSpace(msgs[i].Blueprint.BlueprintPath) == blueprintPath {
			return msgs[i], true
		}
	}
	return api.Message{}, false
}

// SyncBlueprintTranscript updates the blueprint proposal message.
func (m *Service) SyncBlueprintTranscript(ctx context.Context, projectID, blueprintPath string, changedWarning bool) error {
	if m == nil || m.Sessions == nil || m.Getter == nil {
		return nil
	}
	projectID = strings.TrimSpace(projectID)
	blueprintPath = strings.TrimSpace(blueprintPath)
	if projectID == "" || blueprintPath == "" {
		return nil
	}
	active, err := m.Runs.ActiveByProjectForBlueprint(ctx, projectID, blueprintPath)
	if err != nil || active == nil {
		return err
	}
	return m.SyncTranscriptForRun(ctx, active, changedWarning)
}

func (m *Service) SyncTranscriptForRun(ctx context.Context, active *api.WorkflowRun, changedWarning bool) error {
	return m.syncBlueprintTranscript(ctx, m.Controls.RootRun(ctx, active), nil, changedWarning)
}

// vars overrides persisted state during an in-flight transition.
func (m *Service) syncBlueprintTranscript(
	ctx context.Context,
	active *api.WorkflowRun,
	vars map[string]any,
	changedWarning bool,
) error {
	if m == nil || m.Sessions == nil || m.Getter == nil || active == nil {
		return nil
	}
	blueprintPath := strings.TrimSpace(active.BlueprintPath)
	if blueprintPath == "" {
		return nil
	}
	blueprint, err := m.Getter.Get(ctx, active.ProjectID, blueprintPath)
	if err != nil || blueprint == nil {
		return err
	}
	if vars == nil {
		vars, err = m.Runs.GetScaffoldVars(ctx, active.ID)
		if err != nil {
			return err
		}
	}
	awaiting := scaffoldvars.HumanApprovalAwaiting(vars)
	meta := buildBlueprintTranscriptMeta(active, blueprint, awaiting, changedWarning)
	metaCopy := meta

	msgs, err := m.Sessions.GetMessages(ctx, active.SessionID)
	if err != nil {
		return err
	}
	if existing, ok := findBlueprintTranscriptMessage(msgs, blueprintPath); ok {
		patch := existing
		patch.Content = blueprint.Content
		patch.Blueprint = &metaCopy
		patch.Kind = api.MessageKindBlueprint
		if _, err := m.Sessions.UpdateMessage(ctx, active.SessionID, existing.ID, patch); err != nil {
			return err
		}
		m.Transcript.PublishPatch(ctx, active.SessionID, patch)
		return nil
	}

	msg := api.Message{
		ID:            uuid.NewString(),
		Role:          api.MessageRoleAssistant,
		Kind:          api.MessageKindBlueprint,
		Visibility:    api.MessageVisibilityTranscript,
		Content:       blueprint.Content,
		WorkflowRunID: active.ID,
		Blueprint:     &metaCopy,
		CreatedAt:     time.Now().UTC(),
	}
	return m.Transcript.Append(ctx, active.SessionID, msg)
}
