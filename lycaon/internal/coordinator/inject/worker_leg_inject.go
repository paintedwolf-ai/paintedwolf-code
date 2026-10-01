package inject

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
)

// RenderWorkerLegInject renders inject/worker-leg.md for worker L3 prepend.
func RenderWorkerLegInject(ctx context.Context, renderer *prompts.InjectRenderer, sessionID string, legCtx WorkerLegContext) (string, error) {
	if renderer == nil {
		return "", fmt.Errorf("inject renderer not configured")
	}
	data := BuildWorkerLegInjectData(legCtx)
	block, err := anchor.RenderInform(ctx, anchor.InjectWorkerLeg, anchor.MatchContext{Surface: "worker", SessionID: sessionID}, renderer, WorkerLegInjectToMap(data))
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" {
		return "", nil
	}
	if !strings.Contains(block, WorkerLegInjectSentinel) {
		return "", fmt.Errorf("worker-leg inject missing sentinel %q", WorkerLegInjectSentinel)
	}
	return block, nil
}
