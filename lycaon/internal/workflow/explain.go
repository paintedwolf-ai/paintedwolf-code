package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/session"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/publication"
	"github.com/lycaon/lycaon/pkg/api"
)

type Explanations struct {
	Sessions    session.Store
	Transcript  *publication.Messages
	Obligations *Obligations
}

// ObligationPassSource is implemented by obligation kinds whose pending work is
// a full scan pass the person can watch while the host holds the phase.
type ObligationPassSource interface {
	ExplainFullPass(ctx context.Context, run *api.WorkflowRun, params map[string]any) (*api.WorkflowExplainFullPass, error)
}

// appendPhaseExplain writes the phase's explain note once its obligations
// have started, so the note can name the record they produced.
func (m *Explanations) Append(ctx context.Context, run *api.WorkflowRun, def workflowdef.PhaseDef) {
	if m == nil || run == nil || def.Explain == nil || m.Sessions == nil {
		return
	}
	msg := phaseExplainMessage(run, def, m.phaseExplainProgress(ctx, run, def))
	if err := m.Transcript.Append(ctx, run.SessionID, msg); err != nil {
		// The stable message id turns the immediate retry into one logical append.
		if retryErr := m.Transcript.Append(ctx, run.SessionID, msg); retryErr != nil {
			slog.WarnContext(ctx, "append phase explain note", "run_id", run.ID, "phase", def.ID, "error", retryErr)
		}
	}
}

func phaseExplainMessage(run *api.WorkflowRun, def workflowdef.PhaseDef, progress *api.WorkflowExplainProgress) api.Message {
	meta := api.WorkflowExplainMeta{
		PhaseID:  def.ID,
		Summary:  def.Explain.Summary,
		Body:     def.Explain.Body,
		Progress: progress,
	}
	return api.Message{
		ID:              phaseExplainMessageID(run, def.ID),
		Role:            api.MessageRoleSystem,
		Origin:          api.MessageOriginHost,
		Kind:            api.MessageKindWorkflowExplain,
		Visibility:      api.MessageVisibilityTranscript,
		Content:         def.Explain.Summary + "\n\n" + def.Explain.Body,
		WorkflowRunID:   run.ID,
		WorkflowExplain: &meta,
		CreatedAt:       time.Now().UTC(),
	}
}

// phaseExplainMessageID keys one note to one phase entry: a retry of the same
// entry appends the same row, and a later re-entry writes its own.
func phaseExplainMessageID(run *api.WorkflowRun, phaseID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("workflow-explain:%s:%d:%s", run.ID, run.Revision, phaseID))).String()
}

// phaseExplainProgress names the records the phase waits on: the stages it
// binds, and the first watchable record among its obligations. A note without
// either still explains the step.
func (m *Explanations) phaseExplainProgress(ctx context.Context, run *api.WorkflowRun, def workflowdef.PhaseDef) *api.WorkflowExplainProgress {
	var out api.WorkflowExplainProgress
	if stages := def.BoundStages(); len(stages) > 0 {
		out.Topology = &api.WorkflowExplainTopology{Stages: stages}
	}
	out.FullPass = m.obligationFullPass(ctx, run, def)
	if out.Topology == nil && out.FullPass == nil {
		return nil
	}
	return &out
}

func (m *Explanations) obligationFullPass(ctx context.Context, run *api.WorkflowRun, def workflowdef.PhaseDef) *api.WorkflowExplainFullPass {
	for _, ob := range def.OnEnter.Obligations {
		source, ok := m.Obligations.Kinds[ob.Kind].(ObligationPassSource)
		if !ok {
			continue
		}
		pass, err := source.ExplainFullPass(ctx, run, ob.Params)
		if err != nil {
			slog.WarnContext(ctx, "resolve phase explain pass", "run_id", run.ID, "phase", def.ID, "kind", ob.Kind, "error", err)
			continue
		}
		if pass == nil {
			continue
		}
		if pass.RootID == "" {
			pass.RootID = m.sessionWorkspaceRootID(ctx, run.SessionID)
		}
		return pass
	}
	return nil
}

func (m *Explanations) sessionWorkspaceRootID(ctx context.Context, sessionID string) string {
	sess, err := m.Sessions.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return ""
	}
	return strings.TrimSpace(sess.WorkspaceRootID)
}
