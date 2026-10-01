package session

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// MergedWorkerBytesSince sums worker completion block bytes since sinceIdx.
func MergedWorkerBytesSince(history []api.Message, sinceIdx int) (mergedBytes int, workerCount int) {
	for _, env := range TerminalWorkerEnvelopesSince(history, sinceIdx) {
		mergedBytes += len(WorkerEnvelopeBlockText(env))
		workerCount++
	}
	return mergedBytes, workerCount
}

// WorkerEnvelopeBlockText returns the block merged into topology fan_out output.
func WorkerEnvelopeBlockText(env WorkerCompletionEnvelope) string {
	block := strings.TrimSpace(env.Body)
	if block == "" {
		if raw, err := json.Marshal(env.Report); err == nil && string(raw) != "{}" && string(raw) != "null" {
			block = string(raw)
		}
	}
	if block == "" {
		block = strings.TrimSpace(env.Summary)
	}
	return block
}
