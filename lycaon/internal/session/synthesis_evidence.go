package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetSynthesisCurator wires the lite curator for synthesis evidence.
func (m *Manager) SetSynthesisCurator(c llm.Curator) {
	if m == nil {
		return
	}
	m.synthesisCurator = c
}

func (m *Manager) synthesisEvidenceDigest(ctx context.Context, sessionID, surfaceID string) string {
	if m == nil || m.store == nil || m.synthesisCurator == nil {
		return ""
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil || sess.IsWorkerChild() {
		return ""
	}
	state := m.BuildImplementSessionState(ctx, sess)
	if !shouldInjectSynthesisEvidenceBlock(state, surfaceID) {
		return ""
	}
	ctx = curationctx.WithSession(ctx, curationctx.Session{
		SessionID:       sess.ID,
		ProjectID:       sess.ProjectID,
		OwnerPersonID:   sess.OwnerPersonID,
		Posture:         string(sess.Posture),
		Agent:           sess.AgentType,
		ParentSessionID: sess.ParentSessionID,
		ProjectDir:      sess.WorkspacePath,
	})
	var scaffold map[string]any
	var workflowPhase string
	if m.workflows != nil {
		scaffold, _ = m.workflows.Policy.ScaffoldVarsForSession(ctx, sessionID)
		workflowPhase = m.workflows.Policy.CurrentPhase(ctx, sessionID)
	}
	msgs, err := m.store.GetMessages(ctx, sessionID)
	if err != nil || len(msgs) == 0 {
		return scaffoldEvidenceDigest(scaffold)
	}
	since := api.UserIntentBoundary(msgs)
	mergedBytes, workerCount := MergedWorkerBytesSince(msgs, since)
	envelopes := make([]survey.WorkerEnvelope, 0)
	for _, env := range TerminalWorkerEnvelopesSince(msgs, since) {
		envelopes = append(envelopes, surveyWorkerEnvelope(env))
	}
	evidenceOut, err := survey.MaybeCurateSynthesisEvidence(survey.EvidenceInput{
		Ctx:             ctx,
		ParentSessionID: sessionID,
		Envelopes:       envelopes,
		MergedBytes:     mergedBytes,
		WorkerCount:     workerCount,
		Reader:          m.store,
		Curator:         m.synthesisCurator,
		DelegationBrief: survey.DelegationBriefFromScaffold(scaffold, workflowPhase),
		PhaseLabel:      surfaceID,
		AllowCurate:     true,
	})
	if err == nil {
		if digest := strings.TrimSpace(evidenceOut.EvidenceDigest); digest != "" {
			return digest
		}
	}
	return scaffoldEvidenceDigest(scaffold)
}

func scaffoldEvidenceDigest(scaffold map[string]any) string {
	digest, _ := scaffold["evidence_digest"].(string)
	return strings.TrimSpace(digest)
}

func surveyWorkerEnvelope(env WorkerCompletionEnvelope) survey.WorkerEnvelope {
	findings := make([]survey.WorkerFindingSnapshot, 0, len(env.Report.Findings))
	for _, f := range env.Report.Findings {
		findings = append(findings, survey.WorkerFindingSnapshot{
			Path:         f.Path,
			Line:         f.Line,
			Excerpt:      f.Excerpt,
			Note:         f.Note,
			Claim:        f.Claim,
			Adversary:    f.Adversary,
			Precondition: f.Precondition,
			Severity:     f.Severity,
		})
	}
	return survey.WorkerEnvelope{
		JobID:          env.JobID,
		ChildSessionID: env.ChildSessionID,
		AgentType:      env.AgentType,
		Body:           WorkerEnvelopeBlockText(env),
		Report: survey.WorkerReportSnapshot{
			LegStatus:     env.Report.LegStatus,
			Brief:         env.Report.Brief,
			ObjectivesMet: append([]string(nil), env.Report.ObjectivesMet...),
			RemainingRisk: append([]string(nil), env.Report.RemainingRisk...),
			FilesModified: append([]string(nil), env.Report.FilesModified...),
			Findings:      findings,
		},
	}
}

// SynthesisEvidenceForAssembly returns a system inject block for wrapup and adjudication.
func (m *Manager) SynthesisEvidenceForAssembly(ctx context.Context, sess *api.Session, surfaceID string) string {
	if m == nil || sess == nil || sess.IsWorkerChild() {
		return ""
	}
	digest := m.synthesisEvidenceDigest(ctx, sess.ID, surfaceID)
	if digest == "" {
		return ""
	}
	if m.prompts == nil {
		return digest
	}
	inj := prompts.NewInjectRenderer(m.prompts)
	block, err := anchor.RenderInform(ctx, anchor.InjectSynthesisEvidence, anchor.MatchContext{Surface: "coordinator", SessionID: sess.ID}, inj, map[string]any{"evidence_digest": digest})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(block)
}

func shouldInjectSynthesisEvidenceBlock(state surface.ImplementSessionState, surfaceID string) bool {
	if state.WorkersInFlight != 0 {
		return false
	}
	surfaceID = strings.TrimSpace(surfaceID)
	if exit, err := surface.SurfaceExit(surfaceID); err == nil && exit == surface.ExitVerdict {
		return true
	}
	switch surfaceID {
	case "implement_synthesis", "implement_investigate", "":
		return state.WrapupGatesLoaded && state.BatchReadyForSynthesis
	default:
		return false
	}
}
