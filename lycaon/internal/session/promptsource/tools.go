package promptsource

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/history"
	"github.com/lycaon/lycaon/internal/session/naming"
	"github.com/lycaon/lycaon/internal/session/policyfeedback"
	"github.com/lycaon/lycaon/internal/session/processcontrol"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/turnguards"
	sessionverification "github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

type ToolsRepository interface {
	CommitEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content string) (handle string, patchedContent string, err error)
	CommitVisualEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content, artifactID string) (handle string, patchedContent string, err error)
	Get(ctx context.Context, id string) (*api.Session, error)
}
type Tools struct {
	Results      *ToolResults
	DataDir      string
	Feedback     *policyfeedback.Service
	Guards       *turnguards.Service
	History      *history.Service
	Invocations  invocation.Recorder
	Naming       *naming.Service
	Presence     *agentpresence.Tracker
	Processes    *processcontrol.Service
	Projects     project.Registry
	Sessions     ToolsRepository
	Verification *sessionverification.Service
	Visual       visual.Store
	Workers      workeroutcomes.CycleLedger
	Workspace    *sessionscope.Service
}

func (m *Tools) Build() promptloop.ToolsDeps {
	deps := promptloop.ToolsDeps{
		DataDir:       m.DataDir,
		Invocations:   m.Invocations,
		VisualStore:   m.Visual,
		AgentPresence: m.Presence,
		DesignateProjectCover: func(ctx context.Context, projectID, rootSessionID, artifactID string) error {
			if m.Visual == nil || m.Projects == nil {
				return nil
			}
			err := visual.DesignateCover(ctx, m.Visual, project.CoverBinding{Registry: m.Projects},
				func(ctx context.Context, sessionID string) (string, error) {
					sess, err := m.Sessions.Get(ctx, sessionID)
					if err != nil {
						return "", err
					}
					return sess.ProjectID, nil
				},
				visual.DesignateRequest{
					ProjectID:     projectID,
					RootSessionID: rootSessionID,
					ArtifactID:    artifactID,
				},
			)
			if err == nil {
				m.Naming.PublishProject(ctx, projectID)
			}
			return err
		},
		HeldCalls:        m.Processes,
		BlockPlane:       &toolfeedback.BlockPlane{Pipeline: m.Results.Policy.Pipeline, Renderer: m.Feedback.Renderer()},
		BeforeToolRun:    m.Guards.BeforeTool,
		AfterToolRun:     m.Results.AfterTool,
		EnrichToolOutput: m.Results.EnrichTool,
	}
	deps.CommitEvidenceToolResult = func(ctx context.Context, sessionID string, sess *api.Session, toolName string, args map[string]any, content, artifactID string) (string, string, error) {
		if m == nil || sess == nil {
			return "", content, nil
		}
		projectDir, err := m.Workspace.ActivePath(ctx, sess)
		if err != nil {
			return "", content, err
		}
		if artifactID != "" {
			return m.Sessions.CommitVisualEvidenceToolResult(ctx, sessionID, projectDir, toolName, args, content, artifactID)
		}
		return m.Sessions.CommitEvidenceToolResult(ctx, sessionID, projectDir, toolName, args, content)
	}
	deps.RecordSourceRunEvidence = m.Verification.RecordSourceRunEvidence
	deps.ConfirmVerifyResult = m.Verification.ConfirmVerifyResult
	deps.InFlightWorkerRosterNote = func(ctx context.Context, sess *api.Session) string {
		if m == nil || m.Workers == nil || sess == nil {
			return ""
		}
		active, err := workeroutcomes.ParentSessionInFlightWorkers(ctx, m.Workers, sess.ProjectID, sess.ID)
		if err != nil || len(active) == 0 {
			return ""
		}
		note, err := guidance.RenderWorkerInFlightRoster(ctx, guidance.BuildWorkerRosterLines(active))
		if err != nil {
			return ""
		}
		return note
	}
	deps.CompactOversizedToolResults = func(ctx context.Context, sessionID string, sess *api.Session) error {
		if m == nil {
			return nil
		}
		return m.History.ScheduleChunks(ctx, sess)
	}
	deps.CompactToolWire = func(ctx context.Context, sess *api.Session, toolName, content string, opts compaction.CompactToolWireOpts) (string, *api.CompactedChunkMeta) {
		if m == nil {
			return content, nil
		}
		return m.History.ToolWire(ctx, sess, toolName, content, opts)
	}
	deps.ReloadHistory = func(ctx context.Context, sessionID string, sess *api.Session, surfaceID string) ([]api.Message, error) {
		if m == nil {
			return nil, fmt.Errorf("session store not configured")
		}
		return m.History.Reload(ctx, sessionID, sess, surfaceID)
	}

	return deps
}
