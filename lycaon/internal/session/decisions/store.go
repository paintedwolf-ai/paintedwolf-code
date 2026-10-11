package decisions

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// Store stores pending worker decisions.
type Store interface {
	Put(context.Context, api.WorkerDecisionRequest) error
	Get(context.Context, string) (api.WorkerDecisionRequest, bool, error)
	GetByJob(context.Context, string) (api.WorkerDecisionRequest, bool, error)
	Clear(context.Context, string) error
}

func normalizeDecision(decision api.WorkerDecisionRequest) (api.WorkerDecisionRequest, error) {
	decision.ChildSessionID = strings.TrimSpace(decision.ChildSessionID)
	decision.WorkerID = strings.TrimSpace(decision.WorkerID)
	decision.Question = strings.TrimSpace(decision.Question)
	if decision.ChildSessionID == "" || decision.WorkerID == "" || decision.Question == "" {
		return api.WorkerDecisionRequest{}, fmt.Errorf("decision identity is incomplete")
	}
	decision.Options = cleanDecisionStrings(decision.Options)
	decision.ArtifactID = strings.TrimSpace(decision.ArtifactID)
	decision.ArtifactIDs = cleanDecisionStrings(decision.ArtifactIDs)
	if decision.BlockerClass == "" {
		decision.BlockerClass = api.WorkerBlockerDecision
	}
	return decision, nil
}

func cleanDecisionStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
