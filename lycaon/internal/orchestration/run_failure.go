package orchestration

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

const (
	RunFailureCodeExecution        = "TOPOLOGY_EXECUTION_FAILED"
	RunFailureCodePipelineStalled  = "TOPOLOGY_PIPELINE_STALLED"
	RunFailureCodeStageMissing     = "TOPOLOGY_STAGE_MISSING"
	RunFailureCodeStageFailed      = "TOPOLOGY_STAGE_FAILED"
	RunFailureCodeRetryUnavailable = "TOPOLOGY_STAGE_RETRY_UNAVAILABLE"
	RunFailureCodeRetryExhausted   = "TOPOLOGY_STAGE_RETRY_EXHAUSTED"
)

// RunFailure describes why topology settlement could not reach completion.
type RunFailure struct {
	Code      string
	Stage     string
	Retryable bool
	Err       error
}

func (e *RunFailure) Error() string {
	if e == nil {
		return "topology run failed"
	}
	if e.Err == nil {
		return strings.TrimSpace(e.Code)
	}
	return e.Err.Error()
}

func (e *RunFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func newRunFailure(code, stage string, retryable bool, err error) error {
	if err == nil {
		err = fmt.Errorf("topology run failed")
	}
	return &RunFailure{Code: code, Stage: strings.TrimSpace(stage), Retryable: retryable, Err: err}
}

func workflowFailureFor(err error, phase string) api.WorkflowFailure {
	failure := api.WorkflowFailure{
		Code:      RunFailureCodeExecution,
		Message:   "The workflow topology could not complete.",
		Phase:     strings.TrimSpace(phase),
		Retryable: true,
	}
	var runFailure *RunFailure
	if errors.As(err, &runFailure) {
		if code := strings.TrimSpace(runFailure.Code); code != "" {
			failure.Code = code
		}
		failure.Stage = strings.TrimSpace(runFailure.Stage)
		failure.Retryable = runFailure.Retryable
	}
	return failure
}
