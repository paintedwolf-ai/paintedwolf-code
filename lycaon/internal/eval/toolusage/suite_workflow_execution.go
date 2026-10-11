package toolusage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/fseffect"
	sessionobservation "github.com/lycaon/lycaon/internal/session/observation"
	"github.com/lycaon/lycaon/pkg/api"
)

func (c *liveClient) runWorkflowScenario(ctx context.Context, result *CaseReport, spec SuiteCase, progress func(CaseReport) error) error {
	if err := c.waitSessionPrepared(ctx, result.SessionID); err != nil {
		return err
	}
	body, err := json.Marshal(api.StartWorkflowRunRequest{OperationID: uuid.NewString(), WorkflowID: spec.WorkflowID, WorkflowVersion: spec.WorkflowVersion, Request: spec.Prompt})
	if err != nil {
		return err
	}
	request, err := c.newRequest(ctx, http.MethodPost, "/v1/sessions/"+result.SessionID+"/workflow-runs", bytes.NewReader(body))
	if err != nil {
		return err
	}
	run, err := decodeJSON[api.WorkflowRun](c.do(request)) //nolint:bodyclose // decodeJSON closes the body.
	if err != nil {
		return err
	}
	if run.SessionID != result.SessionID || run.WorkflowID != spec.WorkflowID || run.WorkflowVersion != spec.WorkflowVersion || run.ID == "" {
		return &ExecutionFailure{Kind: "harness", Code: "workflow_binding"}
	}
	result.WorkflowRunID = run.ID
	if err := progress(*result); err != nil {
		return err
	}
	return c.waitScenarioCompletion(ctx, result.SessionID, func(ctx context.Context) (bool, error) {
		return c.observeWorkflow(ctx, *result)
	})
}

func (c *liveClient) observeWorkflow(ctx context.Context, result CaseReport) (bool, error) {
	request, err := c.newRequest(ctx, http.MethodGet, "/harness/workflow-execution/"+result.SessionID+"/"+result.WorkflowRunID, nil)
	if err != nil {
		return false, err
	}
	observation, err := decodeJSON[sessionobservation.WorkflowExecutionObservation](c.do(request)) //nolint:bodyclose // decodeJSON closes the body.
	if err != nil {
		return false, err
	}
	if err := validateWorkflowObservation(observation, result); err != nil {
		return false, err
	}
	if observation.Execution.Settled && c.settlementDirectory != "" {
		body, err := json.Marshal(observation)
		if err != nil {
			return false, err
		}
		directory := filepath.Join(c.settlementDirectory, "workflow-settlements", result.SessionID)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return false, err
		}
		_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: directory, Rel: result.WorkflowRunID + ".json"}, Source: bytes.NewReader(body), Mode: 0o600})
		if err != nil {
			return false, err
		}
	}
	return observation.Execution.Settled, nil
}

func validateWorkflowObservation(observation sessionobservation.WorkflowExecutionObservation, result CaseReport) error {
	if observation.Run.ID != result.WorkflowRunID || observation.Run.WorkflowID != result.WorkflowID || observation.Run.SessionID != result.SessionID || observation.Execution.SessionID != result.SessionID {
		return &ExecutionFailure{Kind: "harness", Code: "workflow_binding"}
	}
	if observation.Execution.Settled && len(observation.Execution.Blockers) != 0 {
		return &ExecutionFailure{Kind: "harness", Code: "execution_contract_invalid"}
	}
	return executionTreeFailure(observation.Execution)
}

func settleCapturedWorkflow(capture string, result *CaseReport) error {
	body, err := os.ReadFile(filepath.Join(capture, "workflow-settlements", result.SessionID, result.WorkflowRunID+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var observation sessionobservation.WorkflowExecutionObservation
	if err := json.Unmarshal(body, &observation); err != nil {
		return err
	}
	if err := validateWorkflowObservation(observation, *result); err != nil {
		return err
	}
	if !observation.Execution.Settled {
		return fmt.Errorf("workflow execution is not settled")
	}
	result.Status, result.Failure, result.Error = "review_required", nil, ""
	return nil
}
