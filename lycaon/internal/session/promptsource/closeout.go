package promptsource

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/batchcontrol"
	"github.com/lycaon/lycaon/internal/session/closeoutassembly"
	"github.com/lycaon/lycaon/internal/session/closeoutevidence"
	"github.com/lycaon/lycaon/internal/session/closeouts"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/session/turnguards"
	"github.com/lycaon/lycaon/internal/session/turnnudges"
	"github.com/lycaon/lycaon/pkg/api"
)

type Closeout struct {
	Batch      *batchcontrol.Service
	Closeout   *closeoutassembly.Service
	Closeouts  *closeouts.Service
	Evidence   *closeoutevidence.Service
	Guards     *turnguards.Service
	Hints      *guidance.HintConfig
	Nudges     *turnnudges.Service
	Policy     *policyfacts.Service
	Rejects    *guidance.StaticRejectFormatter
	RenderKick func(context.Context, string, map[string]any) (string, error)
	Reports    ReportDocuments
}

func (m *Closeout) Build() promptloop.CloseoutDeps {
	deps := promptloop.CloseoutDeps{
		RejectFmt:  m.Rejects,
		HintConfig: m.Hints,
		EvaluateContentAnchor: func(ctx context.Context, sess *api.Session, anchor string, segments []oar.ContentSegment, tool string, args map[string]any) (*guidance.Refusal, bool, string, bool) {
			reject, blocked, content, transformed, err := m.Policy.ContentBlock(ctx, sess, anchor, segments, tool, args)
			if err != nil {
				return guidance.NewRefusal("", err.Error()), true, "", false
			}
			return reject, blocked, content, transformed
		},
		EvaluateCloseoutBlock: func(ctx context.Context, sess *api.Session, gc *oar.GuardContext) (*oar.Decision, error) {
			return m.Policy.CloseoutBlock(ctx, sess, gc)
		},
	}
	deps.TurnCloseoutNudge = m.Nudges.Closeout
	deps.IterationRunwayNudge = m.Nudges.IterationRunway
	deps.BeforeFinishNoToolTurn = m.Guards.BeforeFinish
	deps.OnGroundedSynthesisAccepted = func(ctx context.Context, _ *api.Session, sessionID string) {
		m.Batch.AcceptSynthesis(ctx, sessionID)
	}
	deps.ProseCitationGrounding = func(ctx context.Context, sess *api.Session, history []api.Message, _ string, prose, surfaceID string) *api.CitationGrounding {
		if m == nil || sess == nil || sess.IsWorkerChild() {
			return nil
		}
		roots, err := m.Closeout.CitationRoots(ctx, sess)
		if err != nil {
			return nil
		}
		grounding, err := guard.BuildCoordinatorProseCitationGrounding(ctx, m.Evidence, sess, history, prose, surfaceID, roots)
		if err != nil {
			return nil
		}
		return grounding
	}
	deps.EvidenceLedger = m.Evidence
	deps.MaxCloseoutCitationGroundingRetries = limits.DefaultCitationGroundingRetries
	deps.RenderHostKick = m.RenderKick
	deps.BeginCloseoutIntent = m.Closeouts.BeginIntent
	deps.RecordGroundingFriction = m.Closeouts.RecordGroundingFriction
	deps.NoteCloseoutGroundingReject = m.Closeouts.NoteCloseoutGroundingReject
	deps.NoteCoordinatorToolTurn = m.Closeouts.NoteCoordinatorToolTurn
	deps.CloseoutStallState = m.Closeouts.CloseoutStallState

	if m.Reports != nil {
		deps.CheckRunReportDocument = m.Reports.CheckRunReportDocument
	}
	deps.ClearCloseoutStall = m.Closeouts.ClearCloseoutStall
	deps.AssembleLedgerCloseout = func(ctx context.Context, sessionID, surfaceID string, forcedBy []string, draftedContent string, retryCount int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
		return m.Closeout.Assemble(ctx, sessionID, surfaceID, forcedBy, draftedContent, retryCount)
	}
	deps.CitationRoots = func(ctx context.Context, sess *api.Session) evidence.CitationRoots {
		if m == nil || sess == nil {
			return evidence.CitationRoots{}
		}
		roots, err := m.Closeout.CitationRoots(ctx, sess)
		if err != nil {
			return evidence.CitationRoots{}
		}
		return roots
	}

	return deps
}
