package toolexecution_test

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
)

func TestSiblingProgramChildrenKeepSeparateObservedEgressReviews(t *testing.T) {
	executor, manager := gatherExecutor(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	results := make(chan bool, 2)
	for i, child := range []string{"first-child", "second-child"} {
		cmd := confine.EgressCommand{SessionID: "child-session", RootSessionID: "child-session", ToolCallID: "actual-model-call", ActionID: child, ProjectID: "project", ProjectDir: "/fixture/project"}
		host := []string{"first.invalid", "second.invalid"}[i]
		go func() { results <- executor.ResolveEgressForTest(ctx, cmd, host) }()
	}
	for range 2 {
		select {
		case allowed := <-results:
			if !allowed {
				t.Fatal("fixture approval did not authorize its action")
			}
		case <-ctx.Done():
			t.Fatal("child review did not settle")
		}
	}
	if requests := manager.requests(); len(requests) != 2 {
		t.Fatalf("sibling actions combined into one observed-set review: count%d", len(requests))
	}
}
