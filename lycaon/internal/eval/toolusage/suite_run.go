package toolusage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/eval/episode"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/harnessfixture"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type SuiteOptions struct {
	StateDB string
	LiveOptions
	Suite         *Suite
	Label         string
	ExpectedModel string
	Output        string
	WaitForInput  bool
	Progress      func(CaseReport)
}

type SuiteReport struct {
	Version       int          `json:"version"`
	SuiteID       string       `json:"suite_id"`
	SuiteSHA256   string       `json:"suite_sha256"`
	Label         string       `json:"label"`
	ExpectedModel string       `json:"expected_model"`
	Started       time.Time    `json:"started"`
	CaptureDir    string       `json:"capture_dir"`
	WorkDir       string       `json:"work_dir"`
	Cases         []CaseReport `json:"cases"`
	PromptTokens  int          `json:"prompt_tokens"`
	Stopped       string       `json:"stopped,omitempty"`
}

type CaseReport struct {
	WorkflowRunID      string                         `json:"workflow_run_id,omitempty"`
	WorkflowID         string                         `json:"workflow_id,omitempty"`
	Execution          *episode.Execution             `json:"execution,omitempty"`
	TaskAllowance      *store.ModelLimit              `json:"task_allowance,omitempty"`
	Preparation        *harnessfixture.PreludeReceipt `json:"preparation,omitempty"`
	Failure            *ExecutionFailure              `json:"failure,omitempty"`
	PreparedOverlays   *harnessfixture.Evidence       `json:"prepared_overlays,omitempty"`
	Sandbox            *SandboxEvidence               `json:"sandbox,omitempty"`
	AutomaticResponses []AutomaticResponse            `json:"automatic_responses,omitempty"`
	FeedbackRequests   []FeedbackRequest              `json:"feedback_requests,omitempty"`
	CheckpointRequests []wire.CheckpointEvent         `json:"checkpoint_requests,omitempty"`
	Review             *OutcomeReview                 `json:"review,omitempty"`
	ID                 string                         `json:"id"`
	Run                int                            `json:"run"`
	SessionID          string                         `json:"session_id"`
	ProjectDir         string                         `json:"project_dir"`
	FixtureSHA256      string                         `json:"fixture_sha256"`
	DurationMS         int64                          `json:"duration_ms"`
	Status             string                         `json:"status"`
	Error              string                         `json:"error,omitempty"`
	CaptureError       string                         `json:"capture_error,omitempty"`
	Final              string                         `json:"final,omitempty"`
	ArtifactIDs        []string                       `json:"artifact_ids,omitempty"`
	Outcomes           []string                       `json:"outcomes"`
	Diagnostics        []string                       `json:"diagnostics,omitempty"`
	Profile            *Profile                       `json:"profile,omitempty"`
}

// OutcomeReview is supplied by an operator after inspecting the result, not inferred from tool counts.
type OutcomeReview struct {
	Outcome  string `json:"outcome"` // passed | partial | blocked | failed
	Reviewer string `json:"reviewer"`
	Notes    string `json:"notes"`
}

// RunSuite executes fixtures with scripted approvals and a configured provider.
func RunSuite(ctx context.Context, opts SuiteOptions) (SuiteReport, error) {
	if err := validateSuiteOptions(opts); err != nil {
		return SuiteReport{}, err
	}
	client := newLiveClient(strings.TrimRight(opts.BaseURL, "/"), opts.Token)
	client.settlementDirectory = opts.CaptureDir
	client.observeCompletion = client.observeSubmission
	if opts.StateDB != "" {
		db, err := openFailureObserver(opts.StateDB)
		if err != nil {
			return SuiteReport{}, err
		}
		defer func() { _ = db.Close() }()
		client.observeFailure = func(ctx context.Context, sessionID string) error {
			if opts.Suite.TaskAllowance > 0 {
				if err := observeTaskAllowance(ctx, db, sessionID); err != nil {
					return err
				}
			}
			return nil
		}
		client.observeExecution = func(ctx context.Context, sessionID string) (episode.Execution, error) {
			return episode.ReadExecution(ctx, db, sessionID)
		}
	}
	work, err := os.MkdirTemp("", "paintedwolf-projects-")
	if err != nil {
		return SuiteReport{}, err
	}
	report := SuiteReport{Version: 1, SuiteID: opts.Suite.ID, SuiteSHA256: opts.Suite.Digest, Label: opts.Label,
		ExpectedModel: opts.ExpectedModel, Started: time.Now().UTC(), CaptureDir: opts.CaptureDir, WorkDir: work}
	if err := saveSuiteReport(opts.Output, report); err != nil {
		return report, err
	}
	for run := 1; run <= opts.Runs; run++ {
		for _, spec := range opts.Suite.Cases {
			if ctx.Err() != nil {
				report.Stopped = ctx.Err().Error()
				break
			}
			project := filepath.Join(work, fmt.Sprintf("project-%02d", len(report.Cases)+1))
			index := len(report.Cases)
			report.Cases = append(report.Cases, CaseReport{ID: spec.ID, Run: run, Status: "running"})
			result := runSuiteCase(ctx, client, opts, spec, project, run, func(update CaseReport) error {
				report.Cases[index] = update
				if opts.Progress != nil {
					opts.Progress(update)
				}
				return saveSuiteReport(opts.Output, report)
			})
			report.Cases[index] = result
			if result.Profile != nil {
				report.PromptTokens += result.Profile.TokenSpend.PromptTokens
			}
			if result.Status == "error" {
				report.Stopped = "case execution failed; inspect the retained session"
			}
			if err := saveSuiteReport(opts.Output, report); err != nil {
				return report, err
			}
			if report.Stopped != "" {
				break
			}
		}
		if report.Stopped != "" {
			break
		}
	}
	if err := saveSuiteReport(opts.Output, report); err != nil {
		return report, err
	}
	if report.Stopped != "" {
		return report, fmt.Errorf("suite stopped: %s (report: %s)", report.Stopped, opts.Output)
	}
	return report, nil
}

func validateSuiteOptions(opts SuiteOptions) error {
	if !opts.AllowLive {
		return fmt.Errorf("live evaluation spends real tokens; explicitly enable --allow-live")
	}
	if opts.Suite == nil || len(opts.Suite.Cases) == 0 {
		return fmt.Errorf("a nonempty suite is required")
	}
	if opts.Suite.TaskAllowance < 0 || (opts.Suite.TaskAllowance > 0 && opts.StateDB == "") {
		return fmt.Errorf("task allowance requires a nonnegative limit and an observed application store")
	}
	if err := opts.Suite.Unattended.validate(); err != nil {
		return err
	}
	if opts.Suite.Unattended != nil && opts.WaitForInput {
		return fmt.Errorf("an unattended suite cannot wait for operator input")
	}
	if opts.BaseURL == "" || opts.Token == "" || opts.CaptureDir == "" || opts.Output == "" || opts.Label == "" || opts.ExpectedModel == "" {
		return fmt.Errorf("suite requires address, token, capture, output, label, and exact expected model")
	}
	if opts.Runs < 1 || opts.Runs > 10 || opts.Timeout < 0 {
		return fmt.Errorf("suite requires 1–10 runs and a nonnegative time limit")
	}
	if _, err := os.Stat(opts.Output); err == nil {
		return fmt.Errorf("report already exists; select a new --out path")
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, spec := range opts.Suite.Cases {
		if _, err := snapshotFixture(filepath.Join(opts.Suite.root, spec.Project), ""); err != nil {
			return fmt.Errorf("case %s fixture: %w", spec.ID, err)
		}
	}
	return nil
}

func runSuiteCase(ctx context.Context, client *liveClient, opts SuiteOptions, spec SuiteCase, project string, run int, progress func(CaseReport) error) (result CaseReport) {
	start := time.Now()
	result = CaseReport{WorkflowID: spec.WorkflowID, ID: spec.ID, Run: run, ProjectDir: project, Status: "running", Outcomes: spec.Outcomes, Diagnostics: spec.Diagnostics}
	defer func() { result.DurationMS = time.Since(start).Milliseconds() }()
	candidateStarted := false
	fail := func(err error) CaseReport { return failedSuiteCase(result, err, candidateStarted) }
	ctx, cancel := taskContext(ctx, opts.Timeout)
	defer cancel()
	if err := os.MkdirAll(project, 0o700); err != nil {
		return fail(err)
	}
	fixture := filepath.Join(opts.Suite.root, spec.Project)
	digest, err := snapshotFixture(fixture, project)
	if err != nil {
		return fail(err)
	}
	result.FixtureSHA256 = digest
	outsideRoot := ""
	if spec.Sandbox == sandboxWriteRoot {
		outsideRoot, err = client.externalWriteRoot(ctx, opts.CaptureDir, project)
		if err != nil {
			return fail(err)
		}
	}
	if err := prepareSuiteSandbox(ctx, &spec, &result, outsideRoot); err != nil {
		return fail(err)
	}
	if result.Sandbox != nil {
		defer result.Sandbox.close()
	}
	proj, err := client.createProject(ctx, project, "")
	if err != nil {
		return fail(err)
	}
	if spec.WorkflowID != "" {
		if err := client.enableWorkflowFixture(ctx, proj.ID, spec.WorkflowID, spec.WorkflowVersion); err != nil {
			return fail(err)
		}
	}
	sess, err := client.createSession(ctx, wire.CreateSessionRequest{ProjectID: proj.ID})
	if err != nil {
		return fail(err)
	}
	result.SessionID = sess.ID
	if opts.Suite.TaskAllowance > 0 {
		if err := client.scriptedResponse(ctx, "/harness/model-limit", map[string]any{"session_id": sess.ID, "limit": opts.Suite.TaskAllowance}); err != nil {
			return fail(err)
		}
	}
	if err := client.installPrelude(ctx, &spec, &result); err != nil {
		return fail(err)
	}
	if err := client.prepareOverlays(ctx, &spec, &result); err != nil {
		return fail(err)
	}
	if err := progress(result); err != nil {
		return fail(err)
	}
	client.onHumanInput = observeCaseHumanInput(&result, opts.WaitForInput, progress)
	if opts.Suite.Unattended != nil {
		client.onHumanInput = client.unattendedObserver(ctx, &result, *opts.Suite.Unattended, spec.Approvals, progress)
	}
	if result.Sandbox != nil {
		client.onFollowUp = result.Sandbox.beginFollowUp
	}
	defer func() { client.onHumanInput = nil; client.onFollowUp = nil }()
	candidateStarted = true
	var runErr error
	if spec.WorkflowID != "" {
		runErr = client.runWorkflowScenario(ctx, &result, spec, progress)
	} else {
		runErr = client.runScenario(ctx, sess.ID, spec.CorpusTask, opts.Timeout)
	}
	if len(spec.Prelude) != 0 {
		receiptCtx, receiptDone := evidenceContext(ctx)
		receipt, err := client.preludeReceipt(receiptCtx, sess.ID)
		receiptDone()
		if err != nil {
			runErr = errors.Join(&ExecutionFailure{Kind: "harness", Code: "preparation_evidence"}, err, runErr)
		} else {
			result.Preparation = &receipt
		}
	}
	var abortErr error
	if runErr != nil {
		abortErr = client.abortScenario(ctx, sess.ID)
	}

	return collectSuiteResult(ctx, client, opts, spec, result, runErr, abortErr)
}

func finalAnswer(messages []wire.Message) string {
	if message := finalAnswerMessage(messages); message != nil {
		return message.Content
	}
	return ""
}

func finalAnswerMessage(messages []wire.Message) *wire.Message {
	for i := len(messages) - 1; i >= 0; i-- {
		m := &messages[i]
		if wire.IsUserIntentMessage(*m) {
			return nil
		}
		if m.Role == wire.MessageRoleAssistant && len(m.ToolCalls) == 0 && !wire.IsInternalTranscriptMessage(*m) && strings.TrimSpace(m.Content) != "" {
			return m
		}
	}
	return nil
}

func (c *liveClient) abortScenario(ctx context.Context, id string) error {
	ctx, cancel := evidenceContext(ctx)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/sessions/"+id+"/abort", nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("abort session %s: status %d", id, resp.StatusCode)
	}
	return nil
}

func saveSuiteReport(path string, report SuiteReport) error {
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: filepath.Dir(path), Rel: filepath.Base(path)}, Source: bytes.NewReader(append(body, '\n')), Mode: 0o600})
	return err
}
