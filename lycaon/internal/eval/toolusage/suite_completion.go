package toolusage

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	sessionobservation "github.com/lycaon/lycaon/internal/session/observation"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (c *liveClient) observeSubmission(ctx context.Context, sessionID, submissionID string) (bool, error) {
	request, err := c.newRequest(ctx, http.MethodGet, "/harness/execution/"+sessionID+"/"+submissionID, nil)
	if err != nil {
		return false, err
	}
	observation, err := decodeJSON[sessionobservation.ExecutionObservation](c.do(request)) //nolint:bodyclose // decodeJSON closes the body.
	if err != nil {
		return false, err
	}
	if observation.SessionID != sessionID || observation.SubmissionID != submissionID {
		return false, &ExecutionFailure{Kind: "harness", Code: "execution_binding"}
	}
	if err := executionObservationFailure(observation); err != nil {
		return false, err
	}
	if observation.Settled && c.settlementDirectory != "" {
		body, err := json.Marshal(observation)
		if err != nil {
			return false, err
		}
		directory := filepath.Join(c.settlementDirectory, "settlements", sessionID)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return false, err
		}
		_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: directory, Rel: submissionID + ".json"}, Source: bytes.NewReader(body), Mode: 0o600})
		if err != nil {
			return false, err
		}
	}
	return observation.Settled, nil
}

func executionObservationFailure(observation sessionobservation.ExecutionObservation) error {
	if observation.Settled && (!observation.SubmissionStatus.Terminal() || len(observation.Blockers) != 0) {
		return &ExecutionFailure{Kind: "harness", Code: "execution_contract_invalid"}
	}
	return executionTreeFailure(observation)
}

func executionTreeFailure(observation sessionobservation.ExecutionObservation) error {
	for _, row := range observation.Failures {
		failure := submissionFailure(row.ErrorCode)
		if failure.Kind == "provider" || row.SessionID == observation.SessionID {
			return failure
		}
	}
	for _, row := range observation.Sessions {
		if row.ID == observation.SessionID && row.Status == wire.SessionStatusError {
			return &ExecutionFailure{Kind: "application", Code: "session_error"}
		}
	}
	return nil
}
