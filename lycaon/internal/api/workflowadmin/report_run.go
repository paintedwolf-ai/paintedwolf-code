package workflowadmin

import (
	"context"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/report"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
	"net/http"
	"strings"
	"time"
)

func (s *Handler) HandleGetWorkflowRunReport(w http.ResponseWriter, r *http.Request) {
	input, ok, err := s.BuildRunReportInput(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeRunLookupError(w, r, err)
		return
	}
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeReportNotFound, "workflow report is not available for this run")
		return
	}
	s.serveReportPDF(w, r, input)
}

// BuildRunReportInput assembles a workflow run's declared deliverable from
// run-scoped records only; the manifest control gates the document.
func (s *Handler) BuildRunReportInput(ctx context.Context, runID string) (report.ReportInput, bool, error) {
	run, err := s.Workflows.Store.Runs.Get(ctx, runID)
	if err != nil {
		return report.ReportInput{}, false, err
	}
	if run == nil {
		return report.ReportInput{}, false, nil
	}
	if !runstate.IsTerminal(run.Status) {
		return report.ReportInput{}, false, nil
	}

	manifest, reportEnabled, err := s.reportManifest(ctx, run)
	if err != nil {
		return report.ReportInput{}, false, err
	}
	if !reportEnabled {
		return report.ReportInput{}, false, nil
	}

	msgs, err := s.Store.GetMessages(ctx, run.SessionID)
	if err != nil {
		return report.ReportInput{}, false, err
	}
	completion := lastRunCompletionReport(msgs, run.ID)
	if completion == nil {
		return report.ReportInput{}, false, nil
	}
	sess, _ := s.Store.Get(ctx, run.SessionID)

	name := strings.TrimSpace(manifest.Name)
	if name == "" {
		name = strings.TrimSpace(manifest.ID)
	}
	if name == "" {
		name = strings.TrimSpace(run.WorkflowID)
	}

	phaseVerdicts := workflowpresentation.ReviewVerdicts(ctx, s.Workflows.Verdicts, run, manifest)
	claims := workflowpresentation.ReconcileClaims(phaseVerdicts)
	verdicts, channels, verdictURLs := projectVerdicts(phaseVerdicts)
	cites := append(closeoutCitations(completion.Grounding), verdictCitations(verdicts, channels)...)
	evidenceRows, evidenceTotal := evidenceFromGrounding(completion.Grounding, cites)
	inRun := func(m wire.Message) bool { return strings.TrimSpace(m.WorkflowRunID) == run.ID }
	completedAt, err := reportCompletedAt(run)
	if err != nil {
		return report.ReportInput{}, false, err
	}
	findings := reportFindings(completion, manifest.ReportBrief(), claims)
	unreported := reportUnreported(findings, claims)

	input := report.ReportInput{
		Title:            name,
		Headline:         reportHeadline(completion),
		RunID:            run.ID,
		Project:          s.reportProjectLabel(ctx, run.ProjectID),
		StartedAt:        reportStartedAt(run),
		CompletedAt:      completedAt,
		Workflow:         &report.ReportWorkflow{ID: strings.TrimSpace(run.WorkflowID), Version: strings.TrimSpace(run.WorkflowVersion)},
		Workforce:        workforceFor(sess, msgs, inRun),
		Synthesis:        completion.Content,
		Summary:          reportSummary(completion),
		Findings:         findingRows(findings),
		FindingsLabel:    manifest.ReportFindingsLabel(),
		Limits:           reportLimits(completion),
		Brief:            reportBrief(manifest.ReportBrief(), findings, claims, unreported),
		Ask:              reportAsk(completion.CompletionReport),
		UnreportedClaims: len(unreported),
		Defects:          reportDefects(completion.CompletionReport),
		Claims:           reportClaims(claims),
		Verdicts:         verdicts,
		Evidence:         evidenceRows,
		EvidenceTotal:    evidenceTotal,
		Sources:          sourcesFromGrounding(completion.Grounding, verdicts, verdictURLs),
	}

	var scans []wire.CodeScan
	if s.Scans != nil {
		scans, err = s.Scans.ListByWorkflowRunID(ctx, run.ID)
		if err != nil {
			return report.ReportInput{}, false, err
		}
	}
	rows, rules, scanSummary, headSHA := summarizeScans(scans)
	input.ScanRows = rows
	input.ScanRules = rules
	input.Scan = scanSummary
	input.HeadSHA = headSHA

	var account runAccount
	if err := s.workAccount(ctx, &account, run, manifest); err != nil {
		return report.ReportInput{}, false, err
	}
	account.claimAccount(manifest, claims)
	account.scanAccount(scans, completion.CompletionReport, claims, workflowpresentation.RunSetAsides(phaseVerdicts))
	input.Coverage = account.coverage
	input.Gaps = account.gaps
	input.Checks = account.checks
	input.Inventory = account.inventory
	if err := appendCoverageReview(ctx, &input, s.Workflows.Coverage, run, manifest, phaseVerdicts); err != nil {
		return report.ReportInput{}, false, err
	}
	input.Artifacts = s.artifactsForRun(ctx, run, msgs)
	return input, true, nil
}

func (s *Handler) artifactsForRun(ctx context.Context, run *wire.WorkflowRun, msgs []wire.Message) []report.ReportArtifact {
	if s.VisualStore == nil || run == nil {
		return nil
	}
	root := strings.TrimSpace(run.SessionID)
	if root == "" {
		return nil
	}
	ids := artifactIDsForRun(msgs, run.ID)
	if listed, err := s.VisualStore.ListTree(ctx, root); err == nil {
		for _, item := range listed {
			itemRun := strings.TrimSpace(item.WorkflowRunID)
			// Only stamped list rows — unstamped ids come from transcript visuals.
			if itemRun == "" || itemRun != run.ID {
				continue
			}
			ids = append(ids, strings.TrimSpace(item.ID))
		}
	}
	seen := map[string]struct{}{}
	var out []report.ReportArtifact
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		res := s.VisualStore.Resolve(ctx, root, id)
		if !res.IsPresent() {
			continue
		}
		meta := res.Meta()
		caption := strings.TrimSpace(meta.Caption)
		if caption == "" {
			caption = id
		}
		out = append(out, report.ReportArtifact{
			ID:             id,
			Caption:        caption,
			EvidenceHandle: strings.TrimSpace(meta.EvidenceHandle),
			Mime:           strings.TrimSpace(meta.Mime),
			Bytes:          append([]byte(nil), res.Bytes()...),
		})
	}
	return out
}

func artifactIDsForRun(msgs []wire.Message, runID string) []string {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	var out []string
	seen := map[string]struct{}{}
	for _, msg := range msgs {
		if strings.TrimSpace(msg.WorkflowRunID) != runID {
			continue
		}
		if msg.ToolResult == nil || msg.ToolResult.Visual == nil {
			continue
		}
		id := strings.TrimSpace(msg.ToolResult.Visual.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (s *Handler) reportManifest(ctx context.Context, run *wire.WorkflowRun) (workflowdef.Manifest, bool, error) {
	m, err := s.Workflows.Resolver.ForRun(ctx, run)
	if err != nil {
		return workflowdef.Manifest{}, false, err
	}
	available, err := s.Workflows.Presentation.ReportAvailable(ctx, run.ID)
	return m, available, err
}

func (s *Handler) reportProjectLabel(ctx context.Context, projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return projectID
	}
	p, err := s.Projects.Get(ctx, projectID)
	if err != nil || p == nil {
		return projectID
	}
	if name := strings.TrimSpace(p.Name); name != "" {
		return name
	}
	return projectID
}

func reportStartedAt(run *wire.WorkflowRun) string {
	if run.CreatedAt.IsZero() {
		return ""
	}
	return run.CreatedAt.UTC().Format(time.RFC3339)
}

// reportCompletedAt is when the run reached its terminal status, which the
// store records with every terminal transition.
func reportCompletedAt(run *wire.WorkflowRun) (string, error) {
	if run.CompletedAt == nil || run.CompletedAt.IsZero() {
		return "", fmt.Errorf("terminal workflow run %s has no completion time", run.ID)
	}
	return run.CompletedAt.UTC().Format(time.RFC3339), nil
}

// lastRunCompletionReport returns the newest report stating run scope for this
// run. Phase reports of the same run state phase scope and are skipped.
func lastRunCompletionReport(msgs []wire.Message, runID string) *wire.Message {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if strings.TrimSpace(msgs[i].WorkflowRunID) != runID {
			continue
		}
		if !isRenderableCompletionReport(msgs[i]) {
			continue
		}
		return &msgs[i]
	}
	return nil
}

// workforceFor states what produced the work: the session's model, and the
// worker legs that ran, counted by agent type in first-seen order.
func workforceFor(sess *wire.Session, msgs []wire.Message, inRun func(wire.Message) bool) *report.ReportWorkforce {
	out := &report.ReportWorkforce{}
	if sess != nil {
		out.Provider = strings.TrimSpace(sess.ProviderID)
		out.Model = strings.TrimSpace(sess.Model)
	}
	at := map[string]int{}
	for _, msg := range msgs {
		if msg.WorkerSummary == nil || (inRun != nil && !inRun(msg)) {
			continue
		}
		agent := strings.TrimSpace(msg.WorkerSummary.AgentType)
		if agent == "" {
			continue
		}
		i, ok := at[agent]
		if !ok {
			i = len(out.Agents)
			at[agent] = i
			out.Agents = append(out.Agents, report.ReportAgentCount{Type: agent})
		}
		out.Agents[i].Legs++
	}
	if out.Provider == "" && out.Model == "" && len(out.Agents) == 0 {
		return nil
	}
	return out
}
