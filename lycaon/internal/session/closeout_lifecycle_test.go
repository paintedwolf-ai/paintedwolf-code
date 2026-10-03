package session

import (
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func closeoutState(c *closeoutLifecycle, sessionID, rootID string) (closeoutPromptState, closeoutCycleState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prompt, _ := c.prompts.Load(sessionID)
	cycle, _ := c.cycles.Load(rootID)
	cycle.stall.ForcedBy = append([]string(nil), cycle.stall.ForcedBy...)
	return prompt, cycle
}

func seedCloseoutState(c *closeoutLifecycle, sessionID, rootID string) {
	for kind := closeoutDelayKind(0); kind < closeoutDelayKinds; kind++ {
		c.delay(sessionID, rootID, kind, true, 2)
	}
	c.recordFriction(sessionID, rootID)
	c.noteGroundingReject(rootID, "citation", "offender", "retained draft", nil)
	c.noteToolTurn(rootID)
}

func TestCloseoutPromptBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		input     PromptInput
		worker    bool
		newIntent bool
	}{
		{name: "recovery continuation", input: PromptInput{Text: "Keep going", Continuation: true}},
		{name: "host continuation", input: PromptInput{Text: "continue", HostSignal: &PromptHostSignal{}}},
		{name: "human direction", input: PromptInput{Text: "continue"}, newIntent: true},
		{name: "artifact direction", input: PromptInput{ArtifactIDs: []string{"artifact"}}, newIntent: true},
		{name: "attached content", input: PromptInput{ContentParts: []api.MessageContentPart{{Content: "attachment"}}}, newIntent: true},
		{name: "empty input"},
		{name: "worker assignment", input: PromptInput{Text: "assignment"}, worker: true},
		{name: "worker retry", input: PromptInput{Text: "retry", HostSignal: &PromptHostSignal{}}, worker: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, root := newSynthesisDelayManager(t)
			sess := root
			if tc.worker {
				var err error
				sess, err = mgr.store.CreateChild(t.Context(), root, api.SpawnChildRequest{AgentType: "implementer"})
				testutil.FailErr(t, "create worker", err)
				seedCloseoutState(&mgr.closeout, root.ID, root.ID)
			}
			seedCloseoutState(&mgr.closeout, sess.ID, root.ID)
			rootBefore, cycleBefore := closeoutState(&mgr.closeout, root.ID, root.ID)
			mgr.beginCloseoutPrompt(t.Context(), sess, tc.input)
			prompt, cycle := closeoutState(&mgr.closeout, sess.ID, root.ID)
			if prompt != (closeoutPromptState{}) {
				t.Fatalf("prompt budget survived: %+v", prompt)
			}
			wantCycle := cycleBefore
			if tc.newIntent {
				wantCycle = closeoutCycleState{}
			}
			if !reflect.DeepEqual(cycle, wantCycle) {
				t.Fatalf("cycle = %+v, want %+v", cycle, wantCycle)
			}
			if tc.worker {
				rootAfter, _ := closeoutState(&mgr.closeout, root.ID, root.ID)
				if rootAfter != rootBefore {
					t.Fatalf("worker reset root prompt: %+v -> %+v", rootBefore, rootAfter)
				}
			}
		})
	}
}

func TestCloseoutDelaysHaveIndependentBudgets(t *testing.T) {
	var lifecycle closeoutLifecycle
	for kind := closeoutDelayKind(0); kind < closeoutDelayKinds; kind++ {
		if count, delayed := lifecycle.delay("root", "root", kind, false, 2); count != 0 || delayed {
			t.Fatalf("ineligible obligation %d consumed budget", kind)
		}
		for attempt := 0; attempt < 4; attempt++ {
			count, delayed := lifecycle.delay("root", "root", kind, true, 2)
			if count != min(attempt, 2) || delayed != (attempt < 2) {
				t.Fatalf("obligation %d attempt %d: count=%d delayed=%v", kind, attempt, count, delayed)
			}
		}
	}
	prompt, cycle := closeoutState(&lifecycle, "root", "root")
	if prompt.groundingRejects != 6 || cycle.groundingRejects != 6 {
		t.Fatalf("only consumed delays must count as friction: %+v / %+v", prompt, cycle)
	}
}

func TestCloseoutDisposalPreservesOtherScopes(t *testing.T) {
	for _, target := range []string{"worker", "root"} {
		t.Run(target, func(t *testing.T) {
			m := &Manager{}
			seedCloseoutState(&m.closeout, "root", "root")
			seedCloseoutState(&m.closeout, "worker", "root")
			seedCloseoutState(&m.closeout, "other", "other")
			rootPrompt, rootCycle := closeoutState(&m.closeout, "root", "root")
			otherPrompt, otherCycle := closeoutState(&m.closeout, "other", "other")
			m.DisposeSessionResources(t.Context(), target)
			m.DisposeSessionResources(t.Context(), target)
			prompt, cycle := closeoutState(&m.closeout, "root", "root")
			if target == "root" {
				rootPrompt, rootCycle = closeoutPromptState{}, closeoutCycleState{}
			}
			if prompt != rootPrompt || !reflect.DeepEqual(cycle, rootCycle) {
				t.Fatalf("root state after %s disposal = %+v / %+v", target, prompt, cycle)
			}
			if _, ok := m.closeout.prompts.Load("worker"); ok {
				t.Fatal("worker prompt state survived disposal")
			}
			prompt, cycle = closeoutState(&m.closeout, "other", "other")
			if prompt != otherPrompt || !reflect.DeepEqual(cycle, otherCycle) {
				t.Fatal("unrelated root changed")
			}
		})
	}
}

func TestCloseoutRewindClearsEveryBudget(t *testing.T) {
	mgr, id, _ := newCheckpointTestSession(t)
	anchor := appendRewindAsk(t, mgr, id)
	seedCloseoutState(&mgr.closeout, id, id)
	seedCloseoutState(&mgr.closeout, "other", "other")
	otherPrompt, otherCycle := closeoutState(&mgr.closeout, "other", "other")
	_, err := rewindTest(t, mgr, t.Context(), uuid.NewString(), id, anchor)
	testutil.FailErr(t, "rewind closeout state", err)
	prompt, cycle := closeoutState(&mgr.closeout, id, id)
	if prompt != (closeoutPromptState{}) || !reflect.DeepEqual(cycle, closeoutCycleState{}) {
		t.Fatalf("rewound state survived: %+v / %+v", prompt, cycle)
	}
	prompt, cycle = closeoutState(&mgr.closeout, "other", "other")
	if prompt != otherPrompt || !reflect.DeepEqual(cycle, otherCycle) {
		t.Fatal("rewind changed an unrelated root")
	}
}

func TestCloseoutConcurrentRejectsAndDelays(t *testing.T) {
	var lifecycle closeoutLifecycle
	var workers sync.WaitGroup
	for range 64 {
		workers.Go(func() {
			lifecycle.recordFriction("worker", "root")
			lifecycle.delay("root", "root", closeoutVerdictDelay, true, 2)
		})
	}
	workers.Wait()
	prompt, cycle := closeoutState(&lifecycle, "root", "root")
	worker, _ := closeoutState(&lifecycle, "worker", "root")
	if prompt.delays[closeoutVerdictDelay] != 2 || prompt.groundingRejects != 2 || worker.groundingRejects != 64 || cycle.groundingRejects != 66 {
		t.Fatalf("lost or over-consumed budgets: root=%+v worker=%+v cycle=%+v", prompt, worker, cycle)
	}
}

func TestCloseoutRetentionIsBoundedByLifetime(t *testing.T) {
	var lifecycle closeoutLifecycle
	seedCloseoutState(&lifecycle, "old", "old")
	for i := range scopedstore.DefaultEntries {
		id := fmt.Sprintf("session-%d", i)
		seedCloseoutState(&lifecycle, id, id)
	}
	if lifecycle.prompts.Len() != scopedstore.DefaultEntries || lifecycle.cycles.Len() != scopedstore.DefaultEntries {
		t.Fatal("closeout retention exceeded its bounds")
	}
	prompt, cycle := closeoutState(&lifecycle, "old", "old")
	if prompt != (closeoutPromptState{}) || !reflect.DeepEqual(cycle, closeoutCycleState{}) {
		t.Fatalf("old budgets survived eviction: %+v / %+v", prompt, cycle)
	}
	seedCloseoutState(&lifecycle, "root", "root")
	for i := range scopedstore.DefaultEntries {
		lifecycle.recordFriction(fmt.Sprintf("worker-%d", i), "root")
	}
	if !lifecycle.stallState("root").Active {
		t.Fatal("prompt eviction discarded the active root cycle")
	}
}
