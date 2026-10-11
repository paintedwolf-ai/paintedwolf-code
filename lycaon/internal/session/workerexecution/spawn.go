package workerexecution

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/pkg/api"
)

// SpawnChild creates a child session whose messages stay isolated from the parent.
func (m *Service) SpawnChild(ctx context.Context, parentID string, req api.SpawnChildRequest) (*api.Session, error) {
	parent, err := m.store.Get(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Prompt) != "" {
		req.Prompt = guidance.StripHostBlocks(req.Prompt)
	}
	var child *api.Session
	err = m.gate.WithSessionTreeAdmission(ctx, parentID, func() error {
		parentUntrusted, untrustedErr := m.store.SessionUntrustedContentResult(ctx, parentID)
		if untrustedErr != nil {
			return fmt.Errorf("read parent untrusted-content state: %w", untrustedErr)
		}
		parentSecret, exposureErr := m.store.SessionSecretExposure(ctx, parentID)
		if exposureErr != nil {
			return fmt.Errorf("read parent secret-exposure state: %w", exposureErr)
		}
		var createErr error
		child, createErr = m.store.CreateChild(ctx, parent, req)
		if createErr != nil {
			return createErr
		}
		rollbackChild := func(cause error) error {
			deleteErr := m.store.Delete(context.WithoutCancel(ctx), child.ID)
			child = nil
			if deleteErr != nil {
				return errors.Join(cause, fmt.Errorf("remove partially initialized child: %w", deleteErr))
			}
			return cause
		}
		if err := m.assignWorkerModel(ctx, parent, child); err != nil {
			return rollbackChild(err)
		}
		if parentUntrusted {
			if err := m.store.SeedUntrustedContent(ctx, child.ID); err != nil {
				return rollbackChild(fmt.Errorf("inherit untrusted-content state: %w", err))
			}
			child.UntrustedContent = true
		}
		if parentSecret {
			if err := m.store.SeedSecretExposure(ctx, child.ID); err != nil {
				return rollbackChild(fmt.Errorf("inherit secret-exposure state: %w", err))
			}
		}
		m.injectWorkerKickOnSpawn(ctx, child)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return child, nil
}

// A worker keeps one pool assignment across completions and host restarts.
func (m *Service) assignWorkerModel(ctx context.Context, parent, child *api.Session) error {
	if m.router == nil {
		return nil
	}
	selection, err := m.router.WithOverlayRoots(m.workspace.SettingsRoots(ctx, parent)).Select(ctx)
	if err != nil {
		return fmt.Errorf("select worker model: %w", err)
	}
	if err := m.store.UpdateSession(ctx, child.ID, func(s *api.Session) {
		s.ProviderID, s.Model = selection.ProviderID, selection.Model
	}); err != nil {
		return fmt.Errorf("save worker model: %w", err)
	}
	child.ProviderID, child.Model = selection.ProviderID, selection.Model
	return nil
}

// SetWorkerMaxToolLoops stores the child worker loop cap.
func (m *Service) SetWorkerMaxToolLoops(ctx context.Context, childSessionID string, maxToolLoops int) error {
	if m == nil || maxToolLoops <= 0 {
		return nil
	}
	childSessionID = strings.TrimSpace(childSessionID)
	if childSessionID == "" {
		return fmt.Errorf("child session id required")
	}
	return m.store.UpdateSession(ctx, childSessionID, func(s *api.Session) {
		s.MaxToolLoops = maxToolLoops
	})
}

func (m *Service) injectWorkerKickOnSpawn(ctx context.Context, child *api.Session) {
	if m == nil || child == nil || m.prompts == nil {
		return
	}
	data := map[string]string{
		"agent_type": strings.TrimSpace(child.AgentType),
		"leg_id":     "",
		"phase_id":   "",
	}
	if m.workerContext != nil {
		if legCtx, err := m.workerContext.BuildWorkerPromptContext(child.ID, child); err == nil {
			data["agent_type"] = strings.TrimSpace(legCtx.AgentType)
			data["leg_id"] = strings.TrimSpace(legCtx.LegID)
			data["phase_id"] = strings.TrimSpace(legCtx.PhaseID)
		}
	}
	m.guidance.EmitEager(ctx, child.ID, workerSpawnAnchor(data["leg_id"]), data)
}

func workerSpawnAnchor(legID string) anchor.ID {
	if strings.TrimSpace(legID) != "" {
		return anchor.WorkerLegStarted
	}
	return anchor.WorkerTaskStarted
}

// WorkerSummaryFinalizeOpts returns host limits for worker survey bounding after child runs.
func (m *Service) WorkerSummaryFinalizeOpts(ctx context.Context, sess *api.Session) workercloseout.WorkerSummaryFinalizeOpts {
	if m == nil {
		return workercloseout.WorkerSummaryFinalizeOpts{}
	}
	maxChars := m.limits.Compaction(ctx, sess).MaxWorkerSummaryChars
	if maxChars <= 0 {
		maxChars = compaction.DefaultCompactionConfig().MaxWorkerSummaryChars
	}
	return workercloseout.WorkerSummaryFinalizeOpts{
		MaxChars:            maxChars,
		MaxGroundingRetries: m.history.WorkerGroundingRetries(),
		WorkflowHints:       m.workflowHints,
		RenderWorkerKick:    m.RenderKick,
		WorkspaceCheck:      m.WorkspaceCheck,
		Ledger:              m.store,
		Pipeline:            m.pipeline,
		DecisionPending:     m.DecisionPending,
	}
}

// DecisionPending reports whether the child is parked on an unanswered
// request_decision.
func (m *Service) DecisionPending(ctx context.Context, childSessionID string) bool {
	if m == nil || m.decisions == nil || strings.TrimSpace(childSessionID) == "" {
		return false
	}
	_, ok, err := m.decisions.Get(ctx, childSessionID)
	return err == nil && ok
}

func (m *Service) RenderKick(ctx context.Context, kickID string, data map[string]any) (string, error) {
	if m == nil || m.prompts == nil {
		return "", nil
	}
	// kickID is the Binding.render stem queued by Emit / EmitMatch.
	return m.prompts.RenderKick(ctx, strings.TrimSpace(kickID), data)
}
