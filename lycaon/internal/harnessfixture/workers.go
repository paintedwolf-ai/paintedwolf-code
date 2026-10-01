package harnessfixture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

// ScriptedFailureMessage omits private fixture codes from worker failures.
const ScriptedFailureMessage = "The worker could not complete its assignment."

// Codes distinguish scripted worker outcomes in private harness receipts.
const (
	FailureDispatchUnplanned    = "HARNESS_DISPATCH_UNPLANNED"
	FailureScriptExhausted      = "HARNESS_SCRIPT_EXHAUSTED"
	FailureDecisionUnanswered   = "HARNESS_DECISION_UNANSWERED"
	FailureExecution            = "HARNESS_EXECUTION_FAILED"
	FailureDeliveryVerification = "HARNESS_DELIVERY_VERIFICATION_FAILED"
)

// ScriptedFailure is the permanent execution error a failed stage raises.
type ScriptedFailure struct{ Code string }

func (f ScriptedFailure) Error() string { return ScriptedFailureMessage }

// Outcome records one execution transition in a job’s retained history.
type Outcome struct {
	JobID          string `json:"job_id"`
	ChildSessionID string `json:"child_session_id,omitempty"`
	Kind           string `json:"kind"`
	Code           string `json:"code,omitempty"`
	Option         string `json:"option,omitempty"`
	Detail         string `json:"detail,omitempty"`
}

// ReadWorker returns file content and evidence from the child's tool context.
type ReadWorker func(context.Context, *api.Session, *api.WorkerTask, string) (string, string, error)

type childPlan struct {
	ParentSessionID string              `json:"parent_session_id"`
	SeedJobID       string              `json:"seed_job_id,omitempty"`
	Mode            string              `json:"mode"`
	Stages          []WorkerStage       `json:"stages"`
	Executed        int                 `json:"executed"`
	PendingOutcomes map[string]Delivery `json:"pending_outcomes,omitempty"`
	Committed       *executionCommit    `json:"committed,omitempty"`
}

type executionCommit struct {
	Outcome Outcome          `json:"outcome"`
	Result  api.WorkerResult `json:"result"`
}

type dispatchEntry struct {
	DispatchScript
	ChildSessionID string `json:"child_session_id,omitempty"`
}

type dispatchPlan struct {
	ParentSessionID string          `json:"parent_session_id"`
	Dispatches      []dispatchEntry `json:"dispatches"`
}

// Workers uses scripts for registered sessions and the live executor otherwise.
type Workers struct {
	root      string
	sessions  session.Store
	queue     worker.WorkerQueue
	verify    VerifyWorker
	read      ReadWorker
	decisions session.DecisionStore
	fallback  worker.WorkerExecutor
	locks     sync.Map
}

func NewWorkers(root string, sessions session.Store, queue worker.WorkerQueue, verify VerifyWorker, read ReadWorker, decisions session.DecisionStore, fallback worker.WorkerExecutor) (*Workers, error) {
	if !configdir.IsHarnessChannel() || sessions == nil || queue == nil || verify == nil || read == nil || decisions == nil || fallback == nil {
		return nil, fmt.Errorf("scripted workers require an isolated harness and worker services")
	}
	root = filepath.Join(root, "worker-scripts")
	if err := os.MkdirAll(filepath.Join(root, "outcomes"), 0o700); err != nil {
		return nil, err
	}
	return &Workers{root: root, sessions: sessions, queue: queue, verify: verify, read: read, decisions: decisions, fallback: fallback}, nil
}

// Install binds the fixture's scripts to the prepared children and the parent.
func (w *Workers) Install(parent string, setup Setup, evidence Evidence) error {
	if err := setup.Validate(); err != nil {
		return err
	}
	if len(setup.Overlays) != len(evidence.Overlays) {
		return fmt.Errorf("script preparation cardinality differs")
	}
	lock := w.executionLock("parent:" + parent)
	lock.Lock()
	defer lock.Unlock()
	for i, overlay := range setup.Overlays {
		if overlay.Script == nil {
			continue
		}
		seed := evidence.Overlays[i]
		if _, err := uuid.Parse(seed.ChildSessionID); err != nil {
			return err
		}
		plan := childPlan{ParentSessionID: parent, SeedJobID: seed.JobID, Mode: string(api.TaskScopeModeWrite), Stages: overlay.Script.Stages}
		if err := w.write(childPlanName(seed.ChildSessionID), plan); err != nil {
			return err
		}
	}
	if len(setup.Dispatches) == 0 && setup.Policy != WorkerPolicyScripted {
		return nil
	}
	plan := dispatchPlan{ParentSessionID: parent, Dispatches: []dispatchEntry{}}
	for _, dispatch := range setup.Dispatches {
		plan.Dispatches = append(plan.Dispatches, dispatchEntry{DispatchScript: dispatch})
	}
	return w.write(dispatchPlanName(parent), plan)
}

func (w *Workers) executionLock(key string) *sync.Mutex {
	value, _ := w.locks.LoadOrStore(key, &sync.Mutex{})
	return value.(*sync.Mutex)
}

// AbortWorkerRuntime releases resources held by the underlying executor.
func (w *Workers) AbortWorkerRuntime(ctx context.Context, task api.WorkerTask) error {
	return w.fallback.AbortWorkerRuntime(ctx, task)
}

func (w *Workers) Execute(ctx context.Context, task api.WorkerTask, run worker.WorkerRunContext) (api.WorkerResult, error) {
	if err := ctx.Err(); err != nil {
		return api.WorkerResult{}, err
	}
	if task.ChildSessionID == "" {
		if current, ok := w.queue.Get(task.ID); ok && current != nil && current.ParentSessionID == task.ParentSessionID {
			task.ChildSessionID = current.ChildSessionID
		}
	}
	lock := w.executionLock("parent:" + task.ParentSessionID)
	lock.Lock()
	scripted, bound, err := w.prepareExecution(ctx, task)
	lock.Unlock()
	if !scripted {
		return w.fallback.Execute(ctx, task, run)
	}
	if err != nil {
		return api.WorkerResult{}, w.executionError(bound, err)
	}
	childLock := w.executionLock("child:" + bound.ChildSessionID)
	childLock.Lock()
	defer childLock.Unlock()
	if err := ctx.Err(); err != nil {
		return api.WorkerResult{}, err
	}
	var plan childPlan
	if err := w.load(childPlanName(bound.ChildSessionID), &plan); err != nil {
		return api.WorkerResult{}, w.executionError(bound, err)
	}
	result, err := w.runStage(ctx, bound, run, plan)
	if err != nil {
		return api.WorkerResult{}, w.executionError(bound, err)
	}
	return result, nil
}

// prepareExecution reserves a dispatch without holding the parent lock during delivery.
func (w *Workers) prepareExecution(ctx context.Context, task api.WorkerTask) (bool, api.WorkerTask, error) {
	if task.ChildSessionID != "" {
		if _, err := uuid.Parse(task.ChildSessionID); err != nil {
			return true, task, err
		}
		var plan childPlan
		err := w.load(childPlanName(task.ChildSessionID), &plan)
		if errors.Is(err, os.ErrNotExist) {
			var parent dispatchPlan
			if err := w.load(dispatchPlanName(task.ParentSessionID), &parent); errors.Is(err, os.ErrNotExist) {
				return false, task, nil
			} else if err != nil {
				return true, task, err
			}
			return true, task, w.fail(task, FailureDispatchUnplanned)
		}
		if err != nil {
			return true, task, err
		}
		if plan.ParentSessionID != task.ParentSessionID || plan.SeedJobID == task.ID {
			return true, task, fmt.Errorf("worker identity differs")
		}
		return true, task, nil
	}
	var dispatches dispatchPlan
	err := w.load(dispatchPlanName(task.ParentSessionID), &dispatches)
	if errors.Is(err, os.ErrNotExist) {
		return false, task, nil
	}
	if err != nil {
		return true, task, err
	}
	scope := task.EffectiveScope()
	key := dispatchKey(string(scope.Mode), scope.Paths)
	index := -1
	for i, entry := range dispatches.Dispatches {
		if entry.ChildSessionID == "" && entry.Key() == key {
			index = i
			break
		}
	}
	child, err := w.bindChild(ctx, task)
	if err != nil {
		return true, task, err
	}
	task.ChildSessionID = child.ID
	plan := childPlan{ParentSessionID: task.ParentSessionID, Mode: string(scope.Mode)}
	if index >= 0 {
		dispatches.Dispatches[index].ChildSessionID = child.ID
		if err := w.write(dispatchPlanName(task.ParentSessionID), dispatches); err != nil {
			return true, task, err
		}
		plan.Stages = dispatches.Dispatches[index].Stages
	}
	if err := w.write(childPlanName(child.ID), plan); err != nil {
		return true, task, err
	}
	if index < 0 {
		return true, task, w.fail(task, FailureDispatchUnplanned)
	}
	return true, task, nil
}

func (w *Workers) executionError(task api.WorkerTask, err error) error {
	var planned ScriptedFailure
	if errors.As(err, &planned) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// Diagnostic details stay in the harness; the coordinator sees only the worker failure.
	if recordErr := w.record(Outcome{JobID: task.ID, ChildSessionID: task.ChildSessionID, Kind: "error", Code: FailureExecution, Detail: err.Error()}); recordErr != nil {
		return &worker.PermanentExecutionError{Err: ScriptedFailure{Code: FailureExecution}}
	}
	return &worker.PermanentExecutionError{Err: ScriptedFailure{Code: FailureExecution}}
}

func (w *Workers) bindChild(ctx context.Context, task api.WorkerTask) (*api.Session, error) {
	parent, err := w.sessions.Get(ctx, task.ParentSessionID)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return nil, fmt.Errorf("scripted dispatch parent %s not found", task.ParentSessionID)
	}
	child, err := w.sessions.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: task.AgentType, Prompt: task.Prompt, Files: task.Files, WorkerJobID: task.ID})
	if err != nil {
		return nil, err
	}
	if err := w.queue.SetChildSessionID(ctx, task.ID, child.ID); err != nil {
		return nil, err
	}
	return child, nil
}

func (w *Workers) runStage(ctx context.Context, task api.WorkerTask, run worker.WorkerRunContext, plan childPlan) (api.WorkerResult, error) {
	child, err := w.sessions.Get(ctx, task.ChildSessionID)
	if err != nil {
		return api.WorkerResult{}, err
	}
	if child == nil || child.ParentSessionID != task.ParentSessionID {
		return api.WorkerResult{}, fmt.Errorf("worker parent differs")
	}
	if replay, result, err := w.replayCommitted(ctx, task, plan); replay || err != nil {
		return result, err
	}
	receipt := Outcome{JobID: task.ID, ChildSessionID: child.ID}
	var result api.WorkerResult
	if plan.PendingOutcomes != nil {
		option, ok, optionErr := w.answeredOption(ctx, child.ID)
		if optionErr != nil {
			return api.WorkerResult{}, optionErr
		}
		delivery, planned := plan.PendingOutcomes[option]
		if !ok || !planned {
			return api.WorkerResult{}, w.fail(task, FailureDecisionUnanswered)
		}
		result, err = w.deliverWrite(ctx, child, task, delivery)
		plan.PendingOutcomes = nil
		receipt.Kind, receipt.Option = "delivered", option
	} else {
		if plan.Executed >= len(plan.Stages) {
			return api.WorkerResult{}, w.fail(task, FailureScriptExhausted)
		}
		stage := plan.Stages[plan.Executed]
		result, err = w.executeStage(ctx, child, task, run, plan.Mode, stage)
		plan.Executed++
		if stage.Kind == StageNeedsDecision {
			plan.PendingOutcomes = stage.Outcomes
		}
		receipt.Kind, receipt.Code = string(stage.Kind), stage.Code
	}
	var failed ScriptedFailure
	if err != nil {
		if !errors.As(err, &failed) {
			return api.WorkerResult{}, err
		}
		receipt.Kind, receipt.Code = string(StageFailed), failed.Code
	}
	plan.Committed = &executionCommit{Outcome: receipt, Result: result}
	if writeErr := w.write(childPlanName(child.ID), plan); writeErr != nil {
		return api.WorkerResult{}, writeErr
	}
	if recordErr := w.record(receipt); recordErr != nil {
		return api.WorkerResult{}, recordErr
	}
	return result, err
}

// replayCommitted republishes a settled stage's receipt without rerunning it.
func (w *Workers) replayCommitted(ctx context.Context, task api.WorkerTask, plan childPlan) (bool, api.WorkerResult, error) {
	commit := plan.Committed
	if commit == nil || commit.Outcome.JobID != task.ID {
		return false, api.WorkerResult{}, nil
	}
	if plan.PendingOutcomes != nil {
		_, answered, err := w.answeredOption(ctx, task.ChildSessionID)
		if err != nil {
			return false, api.WorkerResult{}, err
		}
		if answered {
			return false, api.WorkerResult{}, nil
		}
	}
	if err := w.record(commit.Outcome); err != nil {
		return true, api.WorkerResult{}, err
	}
	if commit.Outcome.Kind == string(StageFailed) {
		return true, api.WorkerResult{}, &worker.PermanentExecutionError{Err: ScriptedFailure{Code: commit.Outcome.Code}}
	}
	return true, commit.Result, nil
}

func (w *Workers) executeStage(ctx context.Context, child *api.Session, task api.WorkerTask, run worker.WorkerRunContext, mode string, stage WorkerStage) (api.WorkerResult, error) {
	switch stage.Kind {
	case StageComplete:
		if mode == string(api.TaskScopeModeRead) {
			return w.deliverRead(ctx, child, task, run, stage.Findings)
		}
		return w.deliverWrite(ctx, child, task, Delivery{Files: stage.Files, Verify: stage.Verify})
	case StageNeedsDecision:
		if err := w.decisions.Put(ctx, api.WorkerDecisionRequest{WorkerID: task.ID, ChildSessionID: child.ID, Question: stage.Question, Options: stage.Options, BlockerClass: api.WorkerBlockerDecision}); err != nil {
			return api.WorkerResult{}, err
		}
		return api.WorkerResult{Status: string(api.WorkerSummaryStatusNeedsDecision), HostAssembled: true, Summary: stage.Question,
			CompletionReport: &api.WorkerCompletionReport{LegStatus: "blocked", RemainingRisk: []string{"A decision is required before the change can be finished."}}}, nil
	case StageFailed:
		return api.WorkerResult{}, &worker.PermanentExecutionError{Err: ScriptedFailure{Code: stage.Code}}
	}
	return api.WorkerResult{}, fmt.Errorf("unknown worker stage kind %q", stage.Kind)
}

func (w *Workers) answeredOption(ctx context.Context, childID string) (string, bool, error) {
	messages, err := w.sessions.GetMessages(ctx, childID)
	if err != nil {
		return "", false, err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if option, _, ok := worker.DecisionAnswerFromMessage(messages[i]); ok {
			return option, true, nil
		}
	}
	return "", false, nil
}

func (w *Workers) deliverWrite(ctx context.Context, child *api.Session, task api.WorkerTask, delivery Delivery) (api.WorkerResult, error) {
	branch, err := w.queue.ClaimWorkerBranch(ctx, task.ID)
	if err != nil {
		return api.WorkerResult{}, err
	}
	if branch.WorkspaceRoot == "" || branch.WorkspaceRoot == branch.WorkspacePath {
		return api.WorkerResult{}, fmt.Errorf("scripted delivery requires an isolated write branch")
	}
	if err := replaceFiles(branch.WorkspaceRoot, delivery.Files); err != nil {
		return api.WorkerResult{}, err
	}
	proof, err := w.verify(ctx, child, branch, delivery.Verify)
	if err != nil {
		return api.WorkerResult{}, err
	}
	if proof != nil && proof.Verdict == api.SourceVerdictFailed {
		return api.WorkerResult{}, &worker.PermanentExecutionError{Err: ScriptedFailure{Code: FailureDeliveryVerification}}
	}
	if proof == nil || proof.Verdict != api.SourceVerdictPassed {
		return api.WorkerResult{}, fmt.Errorf("scripted delivery verification did not pass")
	}
	return api.WorkerResult{Status: "complete", HostAssembled: true, Summary: "The requested change is complete and the supplied tests pass.",
		CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", FilesModified: sortedPaths(delivery.Files), RemainingRisk: []string{"Changes have not been integrated."}}}, nil
}

func (w *Workers) deliverRead(ctx context.Context, child *api.Session, task api.WorkerTask, run worker.WorkerRunContext, findings []Finding) (api.WorkerResult, error) {
	report := &api.WorkerCompletionReport{LegStatus: "complete"}
	for _, finding := range findings {
		_, content, err := w.read(ctx, child, &task, finding.Path)
		if err != nil {
			return api.WorkerResult{}, err
		}
		excerpt, err := excerptLine(run.ProjectDir, finding.Path, finding.Line, content)
		if err != nil {
			return api.WorkerResult{}, err
		}
		report.Findings = append(report.Findings, api.WorkerCompletionFinding{Path: finding.Path, Line: finding.Line, Excerpt: excerpt, Note: finding.Note})
	}
	return api.WorkerResult{Status: "complete", HostAssembled: true, Summary: "The survey is complete; findings cite the observed lines.", CompletionReport: report}, nil
}

// excerptLine reads the cited line from file bytes, not the JSON tool envelope.
func excerptLine(projectDir, path string, line int, observed string) (string, error) {
	if strings.TrimSpace(observed) == "" {
		return "", fmt.Errorf("finding %s:%d has no read observation", path, line)
	}
	body, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(path))) // #nosec G304 -- fixture-declared project path.
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if line > len(lines) {
		return "", fmt.Errorf("finding %s:%d is beyond the file", path, line)
	}
	excerpt := strings.TrimSpace(lines[line-1])
	if excerpt == "" {
		return "", fmt.Errorf("finding %s:%d is blank", path, line)
	}
	return excerpt, nil
}

// fail retains the fixture code for the graders and returns the neutral failure.
func (w *Workers) fail(task api.WorkerTask, code string) error {
	if err := w.record(Outcome{JobID: task.ID, ChildSessionID: task.ChildSessionID, Kind: string(StageFailed), Code: code}); err != nil {
		return err
	}
	return &worker.PermanentExecutionError{Err: ScriptedFailure{Code: code}}
}

func (w *Workers) record(outcome Outcome) error {
	name := filepath.Join("outcomes", outcome.JobID+".json")
	var history []Outcome
	if err := w.load(name, &history); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(history) > 0 && history[len(history)-1] == outcome {
		return nil
	}
	return w.write(name, append(history, outcome))
}

func childPlanName(childID string) string     { return childID + ".json" }
func dispatchPlanName(parentID string) string { return parentID + ".dispatches.json" }

func (w *Workers) load(name string, value any) error {
	body, err := os.ReadFile(filepath.Join(w.root, name)) // #nosec G304 -- path is inside the harness plan directory.
	if err != nil {
		return err
	}
	return json.Unmarshal(body, value)
}

func (w *Workers) write(name string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: w.root, Rel: name}, Source: bytes.NewReader(body), Mode: 0o600})
	return err
}
