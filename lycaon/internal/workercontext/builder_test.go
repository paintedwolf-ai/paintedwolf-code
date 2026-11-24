package workercontext_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/profiles"
	sessionworkercontext "github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/workercontext"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

type contextTaskLookup struct{ task *api.WorkerTask }

func (l contextTaskLookup) GetLatestByChildSessionID(context.Context, string) (*api.WorkerTask, bool) {
	return l.task, l.task != nil
}

type phaseResolverStub struct{ phase string }

func (p phaseResolverStub) PhaseForRun(context.Context, string) (string, error) {
	return p.phase, nil
}

type scopedPlaybookMatcher struct{}

func (scopedPlaybookMatcher) MatchForAgent(agentID, _, _ string) ([]string, error) {
	if agentID != "team-reviewer" {
		return nil, nil
	}
	return []string{"Use the team's review checklist"}, nil
}

func TestCompositeWorkerContextBuildsFromWorkerRecordAndWorkflowRun(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "worker_context.db")
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-1", "parent-1", testdbseed.DefaultProjectID)
	now := timelayout.Format(time.Now().UTC())
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, created_at, activity_at, updated_at, root_session_id, mode)
		VALUES ('child-1', ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'parent-1', ?, ?, ?, 'parent-1', NULL)
	`, testdbseed.DefaultProjectID, now, now, now)
	testutil.FailErr(t, "insert child session", err)

	records := workerrecords.New(sqlDB)
	coord := workerrecords.Actor{SessionID: "parent-1", Role: workerrecords.RoleCoordinator, PrincipalID: "parent-1"}
	_, err = records.CreateWorker(t.Context(), coord, workerrecords.Op{ID: "op-create"}, workerrecords.WorkerSpec{
		Name:           "api-builder",
		AgentType:      "implementer",
		ScopeMode:      workerrecords.ScopeWrite,
		Brief:          "build api",
		ChildSessionID: "child-1",
		WorkflowRunID:  "run-1",
		Files:          []string{"internal/api.go"},
		Criteria:       []string{"file_modified: internal/api.go"},
	})
	testutil.FailErr(t, "create worker record", err)

	var seenPaths []string
	c := &workercontext.CompositeWorkerContext{
		Records:  records,
		Tasks:    contextTaskLookup{task: &api.WorkerTask{ID: "job-1", ChildSessionID: "child-1", ParentSessionID: "parent-1", AgentType: "implementer"}},
		Runs:     phaseResolverStub{phase: "execute"},
		Topology: func(string) string { return "parallel" },
		AgentsMDChain: func(_ context.Context, _, _ string, paths []string) (api.Message, error) {
			seenPaths = append([]string(nil), paths...)
			return api.Message{Content: "scoped policy"}, nil
		},
		WorkflowIDForRun: func(context.Context, string) string { return "implement" },
		ReviewSubject: func(context.Context, string) (string, string, bool, error) {
			return "api-builder", "res-1", true, nil
		},
	}
	sess := &api.Session{ID: "child-1", ParentSessionID: "parent-1", AgentType: "implementer"}
	got, err := c.BuildWorkerPromptContext(t.Context(), sess.ID, sess)
	testutil.FailErr(t, "build worker prompt context", err)
	if got.LegID != "api-builder" || !got.RequiresIsolation || got.PhaseID != "execute" || got.WorkflowID != "implement" || got.TopologyPattern != "parallel" {
		t.Fatalf("unexpected worker context: %+v", got)
	}
	if len(got.CompletionCriteria) != 1 || got.CompletionCriteria[0] != "file_modified: internal/api.go" {
		t.Fatalf("criteria = %v", got.CompletionCriteria)
	}
	if !reflect.DeepEqual(seenPaths, []string{"internal/api.go"}) || got.ReviewHandle != "res-1" || got.ReviewWorker != "api-builder" || got.AgentsMDMessage.Content != "scoped policy" {
		t.Fatalf("paths=%v review=(%q,%q) policy=%q", seenPaths, got.ReviewWorker, got.ReviewHandle, got.AgentsMDMessage.Content)
	}
}

func TestCompositeWorkerContextToolsAndChecklists(t *testing.T) {
	agents := orchestration.NewMemoryAgentRegistry()
	testutil.FailErr(t, "load agents", orchestration.LoadRequiredAgentRegistry(t.Context(), agents))
	contract, err := prompts.LoadPersonaContract()
	testutil.FailErr(t, "load persona contract", err)
	matcher, err := prompts.LoadPlaybookMatcherEffective(contract)
	testutil.FailErr(t, "load playbook matcher", err)

	want := []string{"command", "edit", "grep", "list_dir", "read", "scan_pack", "write"}
	c := &workercontext.CompositeWorkerContext{
		Tools: workercontext.LegToolListerFunc(func(_ context.Context, _ *api.Session, profileID string) []string {
			if profileID != "implement" {
				return nil
			}
			return append([]string(nil), want...)
		}),
		AgentsFor:  func(*api.Session) profiles.AgentProfileResolver { return agents },
		MatcherFor: func(*api.Session) workercontext.PlaybookMatcherInterface { return matcher },
	}
	sess := &api.Session{ID: "child-impl", ParentSessionID: "parent-1", AgentType: orchestration.ProfileImplementer}
	got, err := c.BuildWorkerPromptContext(t.Context(), sess.ID, sess)
	testutil.FailErr(t, "build worker context", err)
	if !reflect.DeepEqual(got.LegTools, want) {
		t.Fatalf("LegTools = %v want %v", got.LegTools, want)
	}
	if len(got.Checklist) == 0 {
		t.Fatal("expected implementer checklist items")
	}

	scopedAgents := orchestration.NewMemoryAgentRegistry()
	testutil.FailErr(t, "register scoped agent", scopedAgents.Register(agentdef.Profile{ID: "team-reviewer", ToolProfile: "review_readonly"}))
	scoped := &workercontext.CompositeWorkerContext{
		AgentsFor:  func(*api.Session) profiles.AgentProfileResolver { return scopedAgents },
		MatcherFor: func(*api.Session) workercontext.PlaybookMatcherInterface { return scopedPlaybookMatcher{} },
	}
	scopedCtx, err := scoped.BuildWorkerPromptContext(t.Context(), "child-scoped", &api.Session{ID: "child-scoped", ParentSessionID: "parent", AgentType: "team-reviewer"})
	testutil.FailErr(t, "build scoped worker context", err)
	if len(scopedCtx.Checklist) != 1 || scopedCtx.Checklist[0] != "Use the team's review checklist" {
		t.Fatalf("checklist = %v", scopedCtx.Checklist)
	}
}

type digestScans struct{ check func(context.Context) }

func (s digestScans) List(ctx context.Context, _ []string, _ int) ([]api.CodeScan, error) {
	if s.check != nil {
		s.check(ctx)
	}
	return []api.CodeScan{{ID: "ambient-scan", ScannerID: "lycaon-sast", Status: api.CodeScanStatusComplete}}, nil
}

func (s digestScans) ListByWorkflowRunID(ctx context.Context, runID string) ([]api.CodeScan, error) {
	if s.check != nil {
		s.check(ctx)
	}
	if runID != "run-1" {
		return nil, nil
	}
	return []api.CodeScan{{ID: "run-scan", ScannerID: "lycaon-secrets", Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoverageComplete}}, nil
}

func TestCompositeWorkerContextScanDigestAndInvocationPreservation(t *testing.T) {
	agents := orchestration.NewMemoryAgentRegistry()
	testutil.FailErr(t, "load agents", orchestration.LoadRequiredAgentRegistry(t.Context(), agents))
	sess := &api.Session{ID: "child-skeptic", ParentSessionID: "parent-1", AgentType: "skeptic", WorkspacePath: t.TempDir()}
	task := &api.WorkerTask{ID: "job-1", ChildSessionID: sess.ID, ParentSessionID: sess.ParentSessionID, AgentType: sess.AgentType, WorkflowRunID: "run-1"}
	ctx, cancel := context.WithCancel(sessionworkercontext.WithJob(t.Context(), task.ID))
	defer cancel()
	called := false
	c := &workercontext.CompositeWorkerContext{
		Tasks: contextTaskLookup{task: task},
		Scans: digestScans{check: func(got context.Context) {
			if sessionworkercontext.Job(got) != task.ID {
				t.Fatalf("scan context lost job: %q", sessionworkercontext.Job(got))
			}
		}},
		Tools: workercontext.LegToolListerFunc(func(context.Context, *api.Session, string) []string {
			return []string{"scan_query"}
		}),
		AgentsFor: func(*api.Session) profiles.AgentProfileResolver { return agents },
		AgentsMDChain: func(got context.Context, childID, _ string, _ []string) (api.Message, error) {
			called = true
			if childID != sess.ID || sessionworkercontext.Job(got) != task.ID {
				t.Fatalf("policy context lost invocation: child=%q job=%q", childID, sessionworkercontext.Job(got))
			}
			cancel()
			if got.Err() != context.Canceled {
				t.Fatal("policy assembly lost invocation cancellation")
			}
			return api.Message{Content: "worker policy"}, nil
		},
	}
	got, err := c.BuildWorkerPromptContext(ctx, sess.ID, sess)
	testutil.FailErr(t, "build worker context", err)
	digest := strings.Join(got.ScanDigest, "\n")
	if !strings.Contains(digest, "run-scan") || strings.Contains(digest, "ambient-scan") {
		t.Fatalf("digest = %q, want run-bound scan only", digest)
	}
	if !called || got.AgentsMDMessage.Content != "worker policy" {
		t.Fatalf("worker policy missing: called=%v message=%q", called, got.AgentsMDMessage.Content)
	}
}
