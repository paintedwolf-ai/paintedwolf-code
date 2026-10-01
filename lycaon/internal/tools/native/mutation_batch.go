package native

import (
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// Each item commits independently. A later failure retains earlier receipts.
func interruptedMutationBatch(completed any, cause error) (string, error) {
	raw, err := surveyjson.Marshal(struct {
		BatchComplete bool `json:"batch_complete"`
		Completed     any  `json:"completed"`
	}{Completed: completed})
	if err != nil {
		return "", errors.Join(cause, fmt.Errorf("encode completed mutations: %w", err))
	}
	return string(raw), cause
}
