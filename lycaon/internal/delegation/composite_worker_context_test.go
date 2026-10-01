package delegation_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompositeWorkerContextFallsBackWithoutDelegation(t *testing.T) {
	agents := orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(t.Context(), agents); err != nil {
		testutil.FailErr(t, "LoadRequiredAgentRegistry failed", err)
	}
	c := &delegation.CompositeWorkerContext{
		Delegation: &delegation.WorkerContextLoader{Store: delegation.NewMemoryStore()},
		AgentsFor:  func(*api.Session) session.AgentProfileResolver { return agents },
	}
	sess := &api.Session{
		ID:              "child-1",
		ParentSessionID: "parent-1",
		AgentType:       "plan-writer",
	}
	c.Delegation.Tasks = contextTaskLookup{task: workerTaskFor(sess)}
	ctx, err := c.BuildWorkerPromptContext(sess.ID, sess)
	testutil.FailErr(t, "c.BuildWorkerPromptContext failed", err)
	if ctx.AgentType != "plan-writer" {
		t.Fatalf("agent_type = %q want plan-writer", ctx.AgentType)
	}
	if ctx.TopologyPattern != "pipeline" {
		t.Fatalf("topology = %q want pipeline", ctx.TopologyPattern)
	}
}

func TestCompositeWorkerContextPropagatesDelegationFailure(t *testing.T) {
	store := delegation.NewMemoryStore()
	_, err := store.Create(t.Context(), api.Delegation{ID: "delegation-1", ProjectID: "project-1"}, "parent-1", []api.Leg{{
		ID: "leg-1", WorkerID: "job-1", AgentType: "repo-researcher", Status: api.LegStatusDispatched,
	}})
	testutil.FailErr(t, "create delegation", err)
	c := &delegation.CompositeWorkerContext{Delegation: &delegation.WorkerContextLoader{Store: store, Tasks: contextTaskLookup{task: &api.WorkerTask{ID: "job-1", ChildSessionID: "child-1", ParentSessionID: "parent-1", AgentType: "security-reviewer", DelegationID: "delegation-1", LegID: "leg-1"}}}}
	_, err = c.BuildWorkerPromptContext("child-1", &api.Session{
		ID: "child-1", ParentSessionID: "parent-1", AgentType: "security-reviewer",
	})
	if err == nil {
		t.Fatal("delegation failure was treated as task-spawn context")
	}
}

func TestCompositeWorkerContextFallbackAttachesRootAgentsMD(t *testing.T) {
	loader := &delegation.WorkerContextLoader{
		Store: delegation.NewMemoryStore(),
		AgentsMDChain: func(_ context.Context, _ string, _ string, paths []string) (api.Message, error) {
			if len(paths) != 0 {
				t.Fatalf("fallback paths = %v, want root fallback", paths)
			}
			return api.Message{Content: "root policy"}, nil
		},
	}
	c := &delegation.CompositeWorkerContext{Delegation: loader}
	sess := &api.Session{ID: "child-root", ParentSessionID: "parent", AgentType: "plan-writer"}
	c.Delegation.Tasks = contextTaskLookup{task: workerTaskFor(sess)}
	ctx, err := c.BuildWorkerPromptContext(sess.ID, sess)
	testutil.FailErr(t, "BuildWorkerPromptContext", err)
	if ctx.AgentsMDMessage.Content != "root policy" {
		t.Fatalf("AgentsMDBlock = %q", ctx.AgentsMDMessage.Content)
	}
}

func TestWorkerContextLoaderAttachesRootAgentsMDForUnscopedLeg(t *testing.T) {
	store := delegation.NewMemoryStore()
	leg := api.Leg{ID: "leg-1", WorkerID: "job-1", AgentType: "plan-writer", Status: api.LegStatusDispatched}
	_, err := store.Create(t.Context(), api.Delegation{ID: "delegation-1", ProjectID: "project-1"}, "parent-1", []api.Leg{leg})
	testutil.FailErr(t, "create delegation", err)
	loader := &delegation.WorkerContextLoader{
		Store: store,
		Tasks: contextTaskLookup{task: &api.WorkerTask{ID: "job-1", ChildSessionID: "child-1", ParentSessionID: "parent-1", AgentType: "plan-writer", DelegationID: "delegation-1", LegID: "leg-1"}},
		AgentsMDChain: func(_ context.Context, _ string, _ string, paths []string) (api.Message, error) {
			if len(paths) != 0 {
				t.Fatalf("unscoped leg paths = %v, want root fallback", paths)
			}
			return api.Message{Content: "root policy"}, nil
		},
	}
	ctx, err := loader.BuildWorkerPromptContext("child-1", &api.Session{ID: "child-1", ParentSessionID: "parent-1", AgentType: "plan-writer"})
	testutil.FailErr(t, "BuildWorkerPromptContext", err)
	if ctx.AgentsMDMessage.Content != "root policy" {
		t.Fatalf("AgentsMDBlock = %q", ctx.AgentsMDMessage.Content)
	}
}

func TestCompositeWorkerContextLegToolsFromLister(t *testing.T) {
	agents := orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(t.Context(), agents); err != nil {
		testutil.FailErr(t, "LoadRequiredAgentRegistry failed", err)
	}
	want := []string{"command", "edit", "grep", "list_dir", "read", "scan_pack", "write"}
	lister := delegation.LegToolListerFunc(func(_ context.Context, _ *api.Session, profileID string) []string {
		if profileID != "implement" {
			return nil
		}
		return append([]string(nil), want...)
	})
	c := &delegation.CompositeWorkerContext{
		Delegation: &delegation.WorkerContextLoader{Store: delegation.NewMemoryStore()},
		Tools:      lister,
		AgentsFor:  func(*api.Session) session.AgentProfileResolver { return agents },
	}
	sess := &api.Session{
		ID:              "child-impl",
		ParentSessionID: "parent-1",
		AgentType:       orchestration.ProfileImplementer,
	}
	c.Delegation.Tasks = contextTaskLookup{task: workerTaskFor(sess)}
	ctx, err := c.BuildWorkerPromptContext(sess.ID, sess)
	testutil.FailErr(t, "c.BuildWorkerPromptContext failed", err)
	if !reflect.DeepEqual(ctx.LegTools, want) {
		t.Fatalf("LegTools = %v want %v", ctx.LegTools, want)
	}
}

func TestCompositeWorkerContextImplementerTaskSpawnChecklist(t *testing.T) {
	agents := orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(t.Context(), agents); err != nil {
		testutil.FailErr(t, "LoadRequiredAgentRegistry", err)
	}
	contract, err := prompts.LoadPersonaContract()
	testutil.FailErr(t, "prompts.LoadPersonaContract failed", err)
	matcher, err := prompts.LoadPlaybookMatcherEffective(contract)
	testutil.FailErr(t, "prompts.LoadPlaybookMatcherEffective failed", err)
	c := &delegation.CompositeWorkerContext{
		Delegation: &delegation.WorkerContextLoader{Store: delegation.NewMemoryStore()},
		AgentsFor:  func(*api.Session) session.AgentProfileResolver { return agents },
		MatcherFor: func(*api.Session) delegation.PlaybookMatcherInterface { return matcher },
	}
	sess := &api.Session{
		ID:              "child-2",
		ParentSessionID: "parent-1",
		AgentType:       orchestration.ProfileImplementer,
	}
	c.Delegation.Tasks = contextTaskLookup{task: workerTaskFor(sess)}
	ctx, err := c.BuildWorkerPromptContext(sess.ID, sess)
	testutil.FailErr(t, "c.BuildWorkerPromptContext failed", err)
	found := false
	for _, item := range ctx.Checklist {
		if item == "Run tests and the leg's external-state commands via command" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("implementer task spawn checklist missing implement-addenda: %v", ctx.Checklist)
	}
}

type scopedPlaybookMatcher struct{}

func (scopedPlaybookMatcher) MatchForAgent(agentID, _, _ string) ([]string, error) {
	if agentID != "team-reviewer" {
		return nil, nil
	}
	return []string{"Use the team's review checklist"}, nil
}

func TestCompositeWorkerContextUsesScopedAgentAndPlaybookResolvers(t *testing.T) {
	scopedAgents := orchestration.NewMemoryAgentRegistry()
	if err := scopedAgents.Register(agentdef.Profile{
		ID: "team-reviewer", ToolProfile: "review_readonly",
	}); err != nil {
		testutil.FailErr(t, "register scoped agent", err)
	}
	c := &delegation.CompositeWorkerContext{
		Delegation: &delegation.WorkerContextLoader{Store: delegation.NewMemoryStore()},
		AgentsFor:  func(*api.Session) session.AgentProfileResolver { return scopedAgents },
		MatcherFor: func(*api.Session) delegation.PlaybookMatcherInterface { return scopedPlaybookMatcher{} },
	}
	sess := &api.Session{ID: "child-scoped", ParentSessionID: "parent", AgentType: "team-reviewer"}
	c.Delegation.Tasks = contextTaskLookup{task: workerTaskFor(sess)}
	ctx, err := c.BuildWorkerPromptContext(sess.ID, sess)
	testutil.FailErr(t, "BuildWorkerPromptContext", err)
	if len(ctx.Checklist) != 1 || ctx.Checklist[0] != "Use the team's review checklist" {
		t.Fatalf("checklist = %v", ctx.Checklist)
	}
}

type contextTaskLookup struct{ task *api.WorkerTask }

func (l contextTaskLookup) GetLatestByChildSessionID(context.Context, string) (*api.WorkerTask, bool) {
	return l.task, l.task != nil
}
func workerTaskFor(sess *api.Session) *api.WorkerTask {
	return &api.WorkerTask{ID: "job-1", ChildSessionID: sess.ID, ParentSessionID: sess.ParentSessionID, AgentType: sess.AgentType}
}

func TestOrdinaryWorkerDoesNotInheritParentDelegation(t *testing.T) {
	for _, status := range []api.LegStatus{api.LegStatusDispatched, api.LegStatusRunning, api.LegStatusComplete} {
		for _, agent := range []string{"skeptic", "path-explorer"} {
			t.Run(string(status)+"/"+agent, func(t *testing.T) {
				store := delegation.NewMemoryStore()
				_, err := store.Create(t.Context(), api.Delegation{ID: "older", ProjectID: "project"}, "parent", []api.Leg{{ID: "unrelated", WorkerID: "another-job", AgentType: agent, Status: status, CompletionCriteria: []string{"unrelated criteria"}}})
				testutil.FailErr(t, "create earlier delegation", err)
				sess := &api.Session{ID: "child", ParentSessionID: "parent", AgentType: "skeptic"}
				c := &delegation.CompositeWorkerContext{Delegation: &delegation.WorkerContextLoader{Store: store, Tasks: contextTaskLookup{workerTaskFor(sess)}}}
				got, err := c.BuildWorkerPromptContext(sess.ID, sess)
				testutil.FailErr(t, "build ordinary worker context", err)
				if got.LegID != "" || len(got.CompletionCriteria) != 0 {
					t.Fatalf("unrelated leg context leaked: %+v", got)
				}
			})
		}
	}
}

func TestWorkerContextUsesExactJobBinding(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "forward", true: "reverse"}[reverse], func(t *testing.T) {
			store := delegation.NewMemoryStore()
			legs := []api.Leg{
				{ID: "first", WorkerID: "job-first", AgentType: "path-explorer", Status: api.LegStatusDispatched, Files: []string{"first.go"}, CompletionCriteria: []string{"first criteria"}},
				{ID: "second", WorkerID: "job-second", AgentType: "path-explorer", Status: api.LegStatusRunning, Files: []string{"second.go"}, CompletionCriteria: []string{"second criteria"}},
			}
			if reverse {
				legs[0], legs[1] = legs[1], legs[0]
			}
			_, err := store.Create(t.Context(), api.Delegation{ID: "bound", ProjectID: "project"}, "parent", legs)
			testutil.FailErr(t, "create bound delegation", err)
			_, err = store.Create(t.Context(), api.Delegation{ID: "newer", ProjectID: "project"}, "parent", []api.Leg{{ID: "unrelated", AgentType: "skeptic", Status: api.LegStatusDispatched}})
			testutil.FailErr(t, "replace latest parent delegation", err)
			for _, leg := range legs {
				sess := &api.Session{ID: "child-" + leg.ID, ParentSessionID: "parent", AgentType: leg.AgentType}
				job := workerTaskFor(sess)
				job.ID, job.DelegationID, job.LegID = leg.WorkerID, "bound", leg.ID
				loader := &delegation.WorkerContextLoader{Store: store, Tasks: contextTaskLookup{job}, AgentsMDChain: func(_ context.Context, _, _ string, paths []string) (api.Message, error) {
					if !reflect.DeepEqual(paths, leg.Files) {
						t.Fatalf("paths=%v want %v", paths, leg.Files)
					}
					return api.Message{}, nil
				}}
				got, err := loader.BuildWorkerPromptContext(sess.ID, sess)
				testutil.FailErr(t, "build bound worker context", err)
				if got.LegID != leg.ID || !reflect.DeepEqual(got.CompletionCriteria, leg.CompletionCriteria) {
					t.Fatalf("wrong leg context: %+v", got)
				}
			}
		})
	}
}

func TestWorkerContextRejectsBrokenBindings(t *testing.T) {
	cases := []struct {
		name, want string
		mutate     func(*api.WorkerTask, *api.Leg)
	}{
		{"missing delegation", "incomplete delegation binding", func(j *api.WorkerTask, _ *api.Leg) { j.DelegationID = "" }},
		{"missing leg", "incomplete delegation binding", func(j *api.WorkerTask, _ *api.Leg) { j.LegID = "" }},
		{"unknown delegation", "", func(j *api.WorkerTask, _ *api.Leg) { j.DelegationID = "absent" }},
		{"unknown leg", "no matching active leg", func(j *api.WorkerTask, _ *api.Leg) { j.LegID = "absent" }},
		{"different job", "no matching active leg", func(_ *api.WorkerTask, l *api.Leg) { l.WorkerID = "another-job" }},
		{"different leg agent", "no matching active leg", func(_ *api.WorkerTask, l *api.Leg) { l.AgentType = "skeptic" }},
		{"terminal leg", "no matching active leg", func(_ *api.WorkerTask, l *api.Leg) { l.Status = api.LegStatusComplete }},
		{"different parent", "does not match child session", func(j *api.WorkerTask, _ *api.Leg) { j.ParentSessionID = "another-parent" }},
		{"different child", "does not match child session", func(j *api.WorkerTask, _ *api.Leg) { j.ChildSessionID = "another-child" }},
		{"different task agent", "does not match child session", func(j *api.WorkerTask, _ *api.Leg) { j.AgentType = "skeptic" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := &api.Session{ID: "child", ParentSessionID: "parent", AgentType: "path-explorer"}
			job := workerTaskFor(sess)
			job.DelegationID, job.LegID = "bound", "leg"
			leg := api.Leg{ID: "leg", WorkerID: job.ID, AgentType: job.AgentType, Status: api.LegStatusRunning}
			tc.mutate(job, &leg)
			store := delegation.NewMemoryStore()
			_, err := store.Create(t.Context(), api.Delegation{ID: "bound", ProjectID: "project"}, "parent", []api.Leg{leg})
			testutil.FailErr(t, "create delegation", err)
			c := &delegation.CompositeWorkerContext{Delegation: &delegation.WorkerContextLoader{Store: store, Tasks: contextTaskLookup{job}}}
			_, err = c.BuildWorkerPromptContext(sess.ID, sess)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want rejection %q", err, tc.want)
			}
		})
	}
	for _, tasks := range []delegation.WorkerContextTasks{nil, contextTaskLookup{}} {
		c := &delegation.CompositeWorkerContext{Delegation: &delegation.WorkerContextLoader{Store: delegation.NewMemoryStore(), Tasks: tasks}}
		if _, err := c.BuildWorkerPromptContext("child", &api.Session{ID: "child", ParentSessionID: "parent", AgentType: "skeptic"}); err == nil {
			t.Fatal("missing lookup silently fell back")
		}
	}
}

func TestWorkerContextRejectsAnotherParentsDelegation(t *testing.T) {
	store := delegation.NewMemoryStore()
	_, err := store.Create(t.Context(), api.Delegation{ID: "bound", ProjectID: "project"}, "other-parent", []api.Leg{{ID: "leg", WorkerID: "job-1", AgentType: "skeptic", Status: api.LegStatusRunning}})
	testutil.FailErr(t, "create foreign delegation", err)
	sess := &api.Session{ID: "child", ParentSessionID: "parent", AgentType: "skeptic"}
	job := workerTaskFor(sess)
	job.DelegationID, job.LegID = "bound", "leg"
	c := &delegation.CompositeWorkerContext{Delegation: &delegation.WorkerContextLoader{Store: store, Tasks: contextTaskLookup{job}}}
	if _, err = c.BuildWorkerPromptContext(sess.ID, sess); err == nil {
		t.Fatal("another parent's delegation was accepted")
	}
}
