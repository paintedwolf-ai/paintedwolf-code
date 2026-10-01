package toolusage

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/logview"
)

// Wait for coordinator attribution before reading capture metrics.
func awaitSuiteCoordinatorCapture(ctx context.Context, directory, sessionID string) error {
	capture, err := logview.Resolve(directory)
	if err != nil {
		return err
	}
	for {
		rows, err := logview.ReadJSONLFile[logview.LLMRecord](capture.LLMPath)
		if err == nil && hasCoordinatorCapture(rows, sessionID) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("coordinator model attribution unavailable: %w", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func hasCoordinatorCapture(rows []logview.LLMRecord, sessionID string) bool {
	for _, row := range rows {
		if sessionID != "" && row.SessionID == sessionID && row.AgentType == "coordinator" &&
			row.Surface != "" && row.Model != "" && (row.Call == "stream" || row.Call == "complete") {
			return true
		}
	}
	return false
}
