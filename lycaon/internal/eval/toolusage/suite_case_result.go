package toolusage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

func failedSuiteCase(result CaseReport, err error, candidateStarted bool) CaseReport {
	result.Status, result.Error = "error", err.Error()
	var failure *ExecutionFailure
	if errors.As(err, &failure) {
		result.Failure = failure
		if failure.Kind == "model" {
			result.Status = "failed"
		}
	} else if !candidateStarted && retryableLiveError(err) {
		result.Failure = &ExecutionFailure{Kind: "harness", Code: "preparation_transport", Retryable: true}
	} else if !candidateStarted {
		result.Failure = &ExecutionFailure{Kind: "harness", Code: "fixture_preparation", Retryable: false}
	} else {
		result.Failure = &ExecutionFailure{Kind: "harness", Code: "execution_collection"}
	}
	return result
}

func collectSuiteResult(ctx context.Context, client *liveClient, opts SuiteOptions, spec SuiteCase, result CaseReport, runErr, abortErr error) CaseReport {
	fail := func(err error) CaseReport { return failedSuiteCase(result, err, true) }
	sessID := result.SessionID
	readCtx, done := evidenceContext(ctx)
	defer done()
	msgErr := collectSuiteHandoff(readCtx, client, &result)
	if runErr == nil && msgErr == nil && (result.Final != "" || spec.WorkflowID != "") {
		if err := awaitSuiteCoordinatorCapture(readCtx, opts.CaptureDir, sessID); err != nil {
			return fail(err)
		}
	}
	profile, profileErr := ProfileFromCaptureSession(opts.CaptureDir, sessID)
	if profileErr == nil {
		result.Profile = &profile
	}
	if runErr != nil {
		var exhausted interactionExhausted
		if abortErr == nil && errors.As(runErr, &exhausted) {
			result.Status = "interaction_exhausted"
			result.Failure = &ExecutionFailure{Kind: "model", Code: "interaction_exhausted"}
			result.Error = runErr.Error()
			return result
		}
		var checkpoint humanInputRequired
		if abortErr == nil && errors.As(runErr, &checkpoint) {
			result.Status = "blocked"
			result.Failure = &ExecutionFailure{Kind: "harness", Code: "operator_input_required"}
			result.Error = runErr.Error()
			return result
		}
		if abortErr != nil {
			return fail(errors.Join(&ExecutionFailure{Kind: "harness", Code: "abort_failed"}, runErr, abortErr))
		}
		return fail(runErr)
	}
	if msgErr != nil {
		return fail(msgErr)
	}
	if profileErr != nil {
		return fail(profileErr)
	}
	if profile.Model != opts.ExpectedModel {
		return fail(errors.Join(&ExecutionFailure{Kind: "harness", Code: "configuration_mismatch"}, fmt.Errorf("model mismatch: observed %q, expected %q", profile.Model, opts.ExpectedModel)))
	}
	result.Status = "review_required"
	if spec.ReadOnly {
		if err := unchangedFixture(filepath.Join(opts.Suite.root, spec.Project), result.ProjectDir); err != nil {
			result.Status = "failed"
			result.Failure = &ExecutionFailure{Kind: "model", Code: "protected_fixture_changed"}
			result.Error = err.Error()
		}
	}
	if result.Final == "" && spec.WorkflowID == "" {
		result.Status = "failed"
		result.Error = "no user-facing closeout"
		result.Failure = &ExecutionFailure{Kind: "model", Code: "missing_closeout"}
	}
	return result
}

func collectSuiteHandoff(ctx context.Context, client *liveClient, result *CaseReport) error {
	if client.observeExecution != nil {
		execution, err := client.observeExecution(ctx, result.SessionID)
		if err != nil {
			return err
		}
		bindExecutionPresentation(result, execution)
		return nil
	}
	messages, err := client.listMessages(ctx, result.SessionID)
	if err != nil {
		return err
	}
	result.Final = finalAnswer(messages)
	if answer := finalAnswerMessage(messages); answer != nil {
		result.ArtifactIDs = append([]string(nil), answer.ArtifactIDs...)
	}
	return nil
}
