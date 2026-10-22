package trainingcapture

import (
	"context"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/llm/failure"
)

// UtilityHealth contains protocol outcomes only, never utility input or output.
// Collection runners use these facts to stop unhealthy runs before more driving.
type UtilityHealth struct {
	Schema       string `json:"schema"`
	Epoch        string `json:"epoch"`
	Purpose      string `json:"purpose"`
	Success      bool   `json:"success"`
	FinishReason string `json:"finish_reason,omitempty"`
	DecidedAt    string `json:"decided_at"`
}

func ObserveUtility(ctx context.Context, purpose string, err error) {
	root, epoch, _, ok := config()
	if !ok {
		return
	}
	health := UtilityHealth{Schema: "pw-training-health/1", Epoch: epoch, Purpose: purpose,
		Success: err == nil, DecidedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if truncated, ok := failure.AsProviderOutputTruncated(err); ok {
		health.FinishReason = truncated.FinishReason
	}
	if err := writeArtifact(root, health); err != nil {
		slog.WarnContext(ctx, "write training health", "error", err)
	}
}
