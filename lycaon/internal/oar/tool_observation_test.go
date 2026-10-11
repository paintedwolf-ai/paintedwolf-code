package oar

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProf9ConcurrentAdmissionUsesOneOrderedHistory(t *testing.T) {
	l := testLoader(t)
	rule, _, err := l.parseRule("ONCE", ruleDoc(t, map[string]any{
		"id": "ONCE", "flow": []any{"read"}, "when": "true",
	}))
	testutil.FailErr(t, "load activity rule", err)
	store := NewCounterStore()
	pipeline := NewGuardPipeline(NewRuleSet([]*Rule{rule}), l, store)
	for _, anchor := range []string{AnchorToolPreInvoke, AnchorToolHandler, AnchorToolPost, AnchorToolRejected} {
		pipeline.EnableAnchor(anchor)
	}
	var admitted atomic.Int64
	var callers sync.WaitGroup
	for range 32 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			gc := NewGuardContext()
			gc.Session.SessionID = "session"
			gc.ObserveToolCall("read", nil)
			result, err := pipeline.EvaluateBlock(t.Context(), AnchorToolPreInvoke, gc)
			if err != nil {
				t.Errorf("evaluate admission: %v", err)
				return
			}
			if result.Decision == nil {
				admitted.Add(1)
			}
		}()
	}
	callers.Wait()
	if admitted.Load() != 1 || len(store.history["session"]) != 1 {
		t.Fatalf("[OAR-PROF-9] admitted=%d history=%v", admitted.Load(), store.history["session"])
	}
	for _, anchor := range []string{AnchorToolHandler, AnchorToolPost, AnchorToolRejected} {
		gc := NewGuardContext()
		gc.Session.SessionID = "session"
		gc.ObserveToolCall("another", nil)
		_, err := pipeline.EvaluateBlock(t.Context(), anchor, gc)
		testutil.FailErr(t, "evaluate later boundary", err)
	}
	if len(store.history["session"]) != 1 || store.history["session"][0] != "read" {
		t.Fatalf("[OAR-PROF-9] later boundary changed history: %v", store.history)
	}
	store.ForgetSession("session")
	if len(store.occurrences) != 0 || len(store.history) != 0 || len(store.data) != 0 {
		t.Fatalf("[OAR-FIRE-5] closed session retained state: %#v", store)
	}
	gc := NewGuardContext()
	gc.Session.SessionID = "another session"
	gc.ObserveToolCall("read", nil)
	result, err := pipeline.EvaluateBlock(t.Context(), AnchorToolPreInvoke, gc)
	testutil.FailErr(t, "evaluate separate session", err)
	if result.Decision != nil {
		t.Fatalf("[OAR-FIRE-5] activity crossed sessions: %#v", result.Decision)
	}
}

func TestFact6NonToolOccurrenceHasNoFingerprint(t *testing.T) {
	gc := NewGuardContext()
	gc.ObserveToolCall("", nil)
	testutil.FailErr(t, "produce absent fingerprint", gc.Ensure("tool_args_fingerprint"))
	if gc.Invocation.ToolArgsFingerprint != "" {
		t.Fatalf("[OAR-FACT-6] invented non-tool fingerprint %q", gc.Invocation.ToolArgsFingerprint)
	}
}
