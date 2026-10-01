package delegation_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// digestScans holds one scan bound to a run and a newer ambient one.
type digestScans struct{}

func (digestScans) List(context.Context, []string, int) ([]api.CodeScan, error) {
	return []api.CodeScan{{ID: "ambient-scan", ScannerID: "lycaon-sast", Status: api.CodeScanStatusComplete}}, nil
}

func (digestScans) ListByWorkflowRunID(_ context.Context, runID string) ([]api.CodeScan, error) {
	if runID != "run-1" {
		return nil, nil
	}
	return []api.CodeScan{{ID: "run-scan", ScannerID: "lycaon-secrets", Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoverageComplete}}, nil
}

func scanDigestContext(t *testing.T, agent string, tools []string, runID string) string {
	t.Helper()
	sess := &api.Session{ID: "child-" + agent, ParentSessionID: "parent-1", AgentType: agent, WorkspacePath: t.TempDir()}
	task := &api.WorkerTask{ID: "job-1", ChildSessionID: sess.ID, ParentSessionID: sess.ParentSessionID, AgentType: agent, WorkflowRunID: runID}
	agents := orchestration.NewMemoryAgentRegistry()
	testutil.FailErr(t, "load agents", orchestration.LoadRequiredAgentRegistry(t.Context(), agents))
	c := &delegation.CompositeWorkerContext{
		Delegation: &delegation.WorkerContextLoader{Store: delegation.NewMemoryStore(), Tasks: contextTaskLookup{task: task}, Scans: digestScans{}},
		Tools: delegation.LegToolListerFunc(func(context.Context, *api.Session, string) []string {
			return append([]string(nil), tools...)
		}),
		AgentsFor: func(*api.Session) session.AgentProfileResolver { return agents },
	}
	got, err := c.BuildWorkerPromptContext(sess.ID, sess)
	testutil.FailErr(t, "build worker context", err)
	return strings.Join(got.ScanDigest, "\n")
}

// A worker spawned into a workflow run reads the run's bound scans, never the
// project's newest scan, whatever its agent type.
func TestTaskSpawnedWorkerInARunGetsTheRunsScans(t *testing.T) {
	for _, agent := range []string{"skeptic", "code-reviewer"} {
		digest := scanDigestContext(t, agent, []string{"scan_query"}, "run-1")
		if !strings.Contains(digest, "run-scan") || strings.Contains(digest, "ambient-scan") {
			t.Fatalf("%s digest = %q, want the run's scan only", agent, digest)
		}
	}
}

// Outside a run the worker reads the project's newest scan, and a worker with
// no scan tools gets no digest at all.
func TestWorkerScanDigestOutsideARunAndWithoutScanTools(t *testing.T) {
	if digest := scanDigestContext(t, "skeptic", []string{"scan_summary"}, ""); !strings.Contains(digest, "ambient-scan") {
		t.Fatalf("digest = %q, want the project's newest scan", digest)
	}
	if digest := scanDigestContext(t, "plan-writer", []string{"read", "grep"}, "run-1"); digest != "" {
		t.Fatalf("digest = %q, want none for a worker without scan tools", digest)
	}
}
