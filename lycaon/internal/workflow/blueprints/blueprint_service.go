package blueprints

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/blueprintfile"
	"github.com/lycaon/lycaon/internal/session"
	workflowblueprintfiles "github.com/lycaon/lycaon/internal/workflow/blueprintfiles"
	"github.com/lycaon/lycaon/internal/workflow/inputs"
	"github.com/lycaon/lycaon/internal/workflow/lifecycle"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	"github.com/lycaon/lycaon/internal/workflow/publication"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type Service struct {
	Runs        runstate.RunsRepository
	Bindings    runstate.BlueprintsRepository
	Vars        *runstate.Variables
	Sessions    session.Store
	Creator     workflowblueprintfiles.Creator
	Getter      workflowblueprintfiles.Getter
	Scaffold    *inputs.Scaffold
	Starts      *lifecycle.Admission
	Controls    *lifecycle.Commands
	Phases      *workflowphases.Service
	Publication *publication.Runs
	Transcript  *publication.Messages
}

func (m *Service) ResolvePlanForStart(ctx context.Context, req api.StartWorkflowRunRequest, projectID, fixedPath, sourceWorkflowID, sessionID, slashText string) (string, error) {
	if m == nil || m.Creator == nil {
		return "", nil
	}
	titleSource := blueprint.SlugTitle(strings.TrimSpace(req.Request))
	if titleSource == "" {
		titleSource = blueprintTitleFromSlashText(slashText)
	}
	if titleSource == "" {
		titleSource = m.blueprintTitleFromSession(ctx, sessionID)
	}
	title := mintBlueprintTitle(req.BlueprintTitle, titleSource)
	if blueprintPath := strings.TrimSpace(req.BlueprintPath); blueprintPath != "" {
		if err := m.Creator.ResolveDraft(ctx, projectID, blueprintPath); err != nil {
			if errors.Is(err, blueprint.ErrNotFound) {
				return "", runstate.ErrPlanNotFound
			}
			if errors.Is(err, blueprint.ErrInvalidStatus) {
				return "", runstate.ErrPlanNotDraft
			}
			return "", err
		}
		return m.retargetDraft(ctx, projectID, blueprintPath, title)
	}
	if pending := m.Scaffold.TakePendingBlueprintLaunchPath(ctx, sessionID); pending != "" {
		if err := m.Creator.ResolveDraft(ctx, projectID, pending); err != nil {
			if errors.Is(err, blueprint.ErrNotFound) {
				return "", runstate.ErrPlanNotFound
			}
			if errors.Is(err, blueprint.ErrInvalidStatus) {
				return "", runstate.ErrPlanNotDraft
			}
			return "", err
		}
		return m.retargetDraft(ctx, projectID, pending, title)
	}
	createPath := strings.TrimSpace(fixedPath) // options keeps a fixed shared file; plan omits → mint
	path, err := m.Creator.CreateBlueprint(ctx, projectID, title, createPath, sourceWorkflowID)
	if err != nil {
		return "", err
	}
	return m.retargetDraft(ctx, projectID, path, title)
}

func (m *Service) retargetDraft(ctx context.Context, projectID, path, title string) (string, error) {
	if m == nil || m.Creator == nil || !blueprint.IsProvisionalPath(path) || blueprintfile.IsPlaceholderTitle(title) {
		return path, nil
	}
	moved, err := m.Creator.RetargetToTitle(ctx, projectID, path, title)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(moved) == "" {
		return path, nil
	}
	return moved, nil
}

func (m *Service) blueprintTitleFromSession(ctx context.Context, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if m == nil || m.Sessions == nil || sessionID == "" {
		return ""
	}
	msgs, err := m.Sessions.GetMessages(ctx, sessionID)
	if err != nil || len(msgs) == 0 {
		return ""
	}
	boundary := api.UserIntentBoundary(msgs)
	if boundary <= 0 || boundary > len(msgs) {
		return ""
	}
	msg := msgs[boundary-1]
	return blueprint.SlugTitle(msg.Content)
}
