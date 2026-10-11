package toolusage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/eval/episode"
	sessionobservation "github.com/lycaon/lycaon/internal/session/observation"
	"github.com/lycaon/lycaon/internal/session/store"
)

// settleCapturedExecution reads the final outcome after the application closes.
func settleCapturedExecution(ctx context.Context, capture string, result *CaseReport) error {
	database, err := openFailureObserver(filepath.Join(capture, "store.db"))
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
	execution, err := episode.ReadExecution(ctx, database, result.SessionID)
	if errors.Is(err, sql.ErrNoRows) && result.Status == "error" {
		return nil
	}
	if err != nil {
		return err
	}
	allowance, err := episode.ReadAllowance(ctx, database, result.SessionID)
	if err == nil {
		result.TaskAllowance = &allowance
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	bindExecutionPresentation(result, execution)
	if result.Status == "interaction_exhausted" || result.Status == "blocked" || (result.Failure != nil && result.Failure.Kind == "model") {
		return nil
	}
	if result.Failure != nil && result.Failure.Kind != "model" && result.Failure.Code != "execution_collection" {
		return nil
	}
	if result.TaskAllowance != nil && result.TaskAllowance.ExhaustedAttemptID != "" {
		result.Status = "failed"
		result.Failure = &ExecutionFailure{Kind: "model", Code: "task_allowance_exhausted"}
		result.Error = result.Failure.Error()
		return nil
	}
	if execution.Status != "complete" {
		return nil
	}
	if execution.CloseoutAttemptID != "" && execution.Closeout == nil {
		return fmt.Errorf("terminal output has no sealed closeout")
	}
	if result.WorkflowRunID != "" {
		return settleCapturedWorkflow(capture, result)
	}
	if len(execution.SubmissionIDs) == 0 {
		return fmt.Errorf("execution has no admitted user submission")
	}
	submissionID := execution.SubmissionIDs[len(execution.SubmissionIDs)-1]
	body, err := os.ReadFile(filepath.Join(capture, "settlements", result.SessionID, submissionID+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var observation sessionobservation.ExecutionObservation
	if err := json.Unmarshal(body, &observation); err != nil {
		return err
	}
	if observation.SessionID != result.SessionID || observation.SubmissionID != submissionID || !observation.Settled || len(observation.Blockers) != 0 || observation.SubmissionStatus != store.PromptSubmissionComplete {
		return fmt.Errorf("capture settlement does not match completed admission")
	}
	if err := executionObservationFailure(observation); err != nil {
		return err
	}
	result.Status = "review_required"
	result.Failure = nil
	result.Error = ""
	if result.Final == "" && result.WorkflowID == "" {
		result.Status = "failed"
		result.Error = "no user-facing closeout"
		result.Failure = &ExecutionFailure{Kind: "model", Code: "missing_closeout"}
	}
	return nil
}

func bindExecutionPresentation(result *CaseReport, execution episode.Execution) {
	result.Execution = &execution
	result.Final = ""
	result.ArtifactIDs = nil
	if message := execution.Closeout; message != nil && message.Visible && !message.HasToolCalls {
		result.Final = message.Content
		result.ArtifactIDs = message.ArtifactIDs
	}
}
