package security

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

// workerPersona returns the persona delta the production contract declares
// for agentID.
func workerPersona(t *testing.T, agentID string) prompts.PersonaDelta {
	t.Helper()
	contract, err := prompts.LoadPersonaContract()
	testutil.FailErr(t, "load persona contract", err)
	def, ok := contract.Agents[agentID]
	if !ok || strings.TrimSpace(def.Delta.Focus) == "" {
		t.Fatalf("persona contract has no focus for %q", agentID)
	}
	return def.Delta
}

// carriesPersona reports whether a system prompt renders persona's title and focus.
func carriesPersona(system string, persona prompts.PersonaDelta) bool {
	return strings.Contains(system, "# "+persona.RoleTitle+"\n") && strings.Contains(system, persona.Focus)
}

// workerL1s returns the persona system message of each worker-leg request.
func workerL1s(reqs []modelcall.CompletionRequest) []string {
	var out []string
	for _, req := range reqs {
		leg, l1 := false, ""
		for _, msg := range req.Messages {
			if msg.Role != api.MessageRoleSystem {
				continue
			}
			if strings.Contains(msg.Content, inject.WorkerLegInjectSentinel) {
				leg = true
			} else if l1 == "" {
				l1 = msg.Content
			}
		}
		if leg && l1 != "" {
			out = append(out, l1)
		}
	}
	return out
}

type personaHarness struct {
	h      *wiring.Harness
	rec    *llm.RecordingClient
	parent api.Session
	dir    string
}

func newPersonaHarness(t *testing.T) personaHarness {
	t.Helper()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: ".",
		Text:    "implementation complete",
	}}}))
	h := wiring.BuildForTest(t, wiring.WithLLMClient(rec))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)
	dir := t.TempDir()
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, dir)
	testutil.FailErr(t, "CreateHarnessSession failed", err)
	h.SeedProgress(t, context.Background(), parent.ID)
	return personaHarness{h: h, rec: rec, parent: *parent, dir: dir}
}

// dispatch runs one leg of agentType to delivered completion and returns the
// persona system messages of the worker requests it made.
func (p personaHarness) dispatch(t *testing.T, agentType, goal string, completedBefore int) []string {
	t.Helper()
	ctx := context.Background()
	before := len(p.rec.AllRequests())
	_, err := p.h.ToolRegistry.Run(ctx, "task", wiring.TaskToolArgs(agentType, goal),
		p.h.CoordinatorToolContext(t, ctx, &p.parent, p.dir))
	testutil.FailErr(t, "task tool failed", err)
	testutil.WaitFor(t, "the worker job completes and its outcome is delivered", func() bool {
		wiring.FailOnEndedWorker(t, p.h, ctx, p.parent.ProjectID)
		jobs, err := p.h.Workers.Queue.ListBySession(ctx, p.parent.ProjectID, p.parent.ID, api.WorkerStatusComplete)
		testutil.FailErr(t, "list completed jobs", err)
		if len(jobs) != completedBefore+1 {
			return false
		}
		pending, err := p.h.Workers.Queue.ListPendingOutcomes(ctx)
		testutil.FailErr(t, "list pending outcomes", err)
		return len(pending) == 0
	})
	l1s := workerL1s(p.rec.AllRequests()[before:])
	if len(l1s) == 0 {
		t.Fatalf("%s leg made no worker request", agentType)
	}
	return l1s
}

func TestDelegateImplementerPromptContainsPersona(t *testing.T) {
	p := newPersonaHarness(t)
	implementer := workerPersona(t, orchestration.ProfileImplementer)
	for _, l1 := range p.dispatch(t, orchestration.ProfileImplementer, "implement feature", 0) {
		if !carriesPersona(l1, implementer) {
			t.Fatalf("implementer worker L1 lacks the contract's title and focus %q:\n%s", implementer.Focus, l1)
		}
	}
}

func TestDelegatePlanReviewerDistinctFromImplementer(t *testing.T) {
	p := newPersonaHarness(t)
	implementer := workerPersona(t, orchestration.ProfileImplementer)
	reviewer := workerPersona(t, "plan-reviewer")

	implL1 := p.dispatch(t, orchestration.ProfileImplementer, "implement feature", 0)[0]
	reviewL1 := p.dispatch(t, "plan-reviewer", "review plan", 1)[0]

	if implL1 == reviewL1 {
		t.Fatal("plan-reviewer and implementer legs received the same L1 system message")
	}
	if !carriesPersona(reviewL1, reviewer) || carriesPersona(reviewL1, implementer) {
		t.Fatalf("plan-reviewer L1 does not carry exactly its own persona:\n%s", reviewL1)
	}
	if !carriesPersona(implL1, implementer) || carriesPersona(implL1, reviewer) {
		t.Fatalf("implementer L1 does not carry exactly its own persona:\n%s", implL1)
	}
}
