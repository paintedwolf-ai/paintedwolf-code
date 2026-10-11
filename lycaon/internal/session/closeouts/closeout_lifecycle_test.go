package closeouts

import (
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func closeoutState(c *Service, sessionID, rootID string) (closeoutPromptState, closeoutCycleState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prompt, _ := c.prompts.Load(sessionID)
	cycle, _ := c.cycles.Load(rootID)
	cycle.stall.ForcedBy = append([]string(nil), cycle.stall.ForcedBy...)
	return prompt, cycle
}

func seedCloseoutState(c *Service, sessionID, rootID string) {
	for kind := DelayKind(0); kind < DelayKinds; kind++ {
		c.Delay(sessionID, rootID, kind, true, 2)
	}
	c.recordFriction(sessionID, rootID)
	c.noteGroundingReject(rootID, "citation", "offender", "retained draft", nil)
	c.noteToolTurn(rootID)
}

func TestCloseoutPromptBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		input     promptinput.Input
		worker    bool
		newIntent bool
	}{
		{name: "recovery continuation", input: promptinput.Input{Text: "Keep going", Continuation: true}},
		{name: "host continuation", input: promptinput.Input{Text: "continue", HostSignal: &promptinput.HostSignal{}}},
		{name: "human direction", input: promptinput.Input{Text: "continue"}, newIntent: true},
		{name: "artifact direction", input: promptinput.Input{ArtifactIDs: []string{"artifact"}}, newIntent: true},
		{name: "attached content", input: promptinput.Input{ContentParts: []api.MessageContentPart{{Content: "attachment"}}}, newIntent: true},
		{name: "empty input"},
		{name: "worker assignment", input: promptinput.Input{Text: "assignment"}, worker: true},
		{name: "worker retry", input: promptinput.Input{Text: "retry", HostSignal: &promptinput.HostSignal{}}, worker: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, root := newSynthesisDelayManager(t)
			sess := root
			if tc.worker {
				var err error
				sess, err = mgr.store.(*store.Memory).CreateChild(t.Context(), root, api.SpawnChildRequest{AgentType: "implementer"})
				testutil.FailErr(t, "create worker", err)
				seedCloseoutState(mgr, root.ID, root.ID)
			}
			seedCloseoutState(mgr, sess.ID, root.ID)
			rootBefore, cycleBefore := closeoutState(mgr, root.ID, root.ID)
			mgr.BeginPrompt(t.Context(), sess, tc.input)
			prompt, cycle := closeoutState(mgr, sess.ID, root.ID)
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
				rootAfter, _ := closeoutState(mgr, root.ID, root.ID)
				if rootAfter != rootBefore {
					t.Fatalf("worker reset root prompt: %+v -> %+v", rootBefore, rootAfter)
				}
			}
		})
	}
}

func TestCloseoutDelaysHaveIndependentBudgets(t *testing.T) {
	var lifecycle Service
	for kind := DelayKind(0); kind < DelayKinds; kind++ {
		if count, delayed := lifecycle.Delay("root", "root", kind, false, 2); count != 0 || delayed {
			t.Fatalf("ineligible obligation %d consumed budget", kind)
		}
		for attempt := 0; attempt < 4; attempt++ {
			count, delayed := lifecycle.Delay("root", "root", kind, true, 2)
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
			m := &Service{}
			seedCloseoutState(m, "root", "root")
			seedCloseoutState(m, "worker", "root")
			seedCloseoutState(m, "other", "other")
			rootPrompt, rootCycle := closeoutState(m, "root", "root")
			otherPrompt, otherCycle := closeoutState(m, "other", "other")
			m.Forget(target)
			m.Forget(target)
			prompt, cycle := closeoutState(m, "root", "root")
			if target == "root" {
				rootPrompt, rootCycle = closeoutPromptState{}, closeoutCycleState{}
			}
			if prompt != rootPrompt || !reflect.DeepEqual(cycle, rootCycle) {
				t.Fatalf("root state after %s disposal = %+v / %+v", target, prompt, cycle)
			}
			if _, ok := m.prompts.Load("worker"); ok {
				t.Fatal("worker prompt state survived disposal")
			}
			prompt, cycle = closeoutState(m, "other", "other")
			if prompt != otherPrompt || !reflect.DeepEqual(cycle, otherCycle) {
				t.Fatal("unrelated root changed")
			}
		})
	}
}

func TestCloseoutRewindClearsEveryBudget(t *testing.T) {
	mgr := New(nil)
	seedCloseoutState(mgr, "root", "root")
	seedCloseoutState(mgr, "child", "root")
	seedCloseoutState(mgr, "other", "other")
	otherPrompt, otherCycle := closeoutState(mgr, "other", "other")
	mgr.Rewind("child", "root")
	for _, id := range []string{"root", "child"} {
		p, c := closeoutState(mgr, id, "root")
		if p != (closeoutPromptState{}) || !reflect.DeepEqual(c, closeoutCycleState{}) {
			t.Fatalf("rewound state survived: %+v/%+v", p, c)
		}
	}
	p, c := closeoutState(mgr, "other", "other")
	if p != otherPrompt || !reflect.DeepEqual(c, otherCycle) {
		t.Fatal("rewind changed unrelated root")
	}
}
func TestCloseoutConcurrentRejectsAndDelays(t *testing.T) {
	var lifecycle Service
	var workers sync.WaitGroup
	for range 64 {
		workers.Go(func() {
			lifecycle.recordFriction("worker", "root")
			lifecycle.Delay("root", "root", VerdictDelay, true, 2)
		})
	}
	workers.Wait()
	prompt, cycle := closeoutState(&lifecycle, "root", "root")
	worker, _ := closeoutState(&lifecycle, "worker", "root")
	if prompt.delays[VerdictDelay] != 2 || prompt.groundingRejects != 2 || worker.groundingRejects != 64 || cycle.groundingRejects != 66 {
		t.Fatalf("lost or over-consumed budgets: root=%+v worker=%+v cycle=%+v", prompt, worker, cycle)
	}
}

func TestCloseoutRetentionIsBoundedByLifetime(t *testing.T) {
	var lifecycle Service
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

func TestClearStallPreservesBudgetsAndSnapshotIsolation(t *testing.T) {
	m, sess := newSynthesisDelayManager(t)
	seedCloseoutState(m, sess.ID, sess.ID)
	beforePrompt, beforeCycle := closeoutState(m, sess.ID, sess.ID)
	codes := m.CloseoutStallState(t.Context(), sess.ID).ForcedBy
	codes[0] = "caller mutation"
	if got := m.CloseoutStallState(t.Context(), sess.ID).ForcedBy[0]; got != "citation" {
		t.Fatalf("snapshot changed state: %q", got)
	}
	m.ClearCloseoutStall(t.Context(), sess.ID)
	p, c := closeoutState(m, sess.ID, sess.ID)
	beforeCycle.stall = closeoutStall{}
	if p != beforePrompt || !reflect.DeepEqual(c, beforeCycle) {
		t.Fatalf("commit changed budgets: %+v/%+v", p, c)
	}
}
