package settings

import (
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
)

func TestGateBuilderSealPreservesSessionState(t *testing.T) {
	t.Parallel()
	builder, handle := NewGateBuilder(&ApprovalStore{})
	built := builder.Seal()
	gate := built.(*RuleApprovalGate)
	if !gate.grants.put(hitl.ApprovalGrant{
		ID: "task-grant", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: "session",
	}) {
		t.Fatal("install task grant")
	}
	if _, ok := handle.PutAskQuiet(hitl.AskQuiet{ChatSessionID: "session", Key: "reason"}, 0); !ok {
		t.Fatal("install ask quiet")
	}
	if builder.Seal() != built {
		t.Fatal("repeated seal replaced the gate")
	}
	if grants := handle.ListGrants("session"); len(grants) != 1 || grants[0].ID != "task-grant" {
		t.Fatalf("task grants after seal = %+v", grants)
	}
	if _, ok := handle.AskQuietLive("session", "reason"); !ok {
		t.Fatal("repeated seal discarded ask quiet")
	}
}

func TestGateBuilderRejectsLateProducers(t *testing.T) {
	t.Parallel()
	sources := NoSources()
	cases := map[string]func(*GateBuilder){
		"detections":     func(b *GateBuilder) { b.WithDetections(sources.Detections) },
		"pins":           func(b *GateBuilder) { b.WithPins(sources.Pins) },
		"approval rules": func(b *GateBuilder) { b.WithApprovalRules(sources.ApprovalRules) },
	}
	for name, wire := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			builder, handle := NewGateBuilder(nil)
			built := builder.Seal()
			defer func() {
				if recover() == nil {
					t.Error("late producer did not reject sealed wiring")
				}
				if handle.inner() != built {
					t.Error("late producer replaced the gate")
				}
			}()
			wire(builder)
		})
	}
}

func TestGateBuilderConcurrentSealSharesGate(t *testing.T) {
	t.Parallel()
	builder, handle := NewGateBuilder(nil)
	const callers = 16
	results := make(chan hitl.ApprovalGate, callers)
	var workers sync.WaitGroup
	for range callers {
		workers.Go(func() { results <- builder.Seal() })
	}
	workers.Wait()
	close(results)
	for built := range results {
		if built != handle.inner() {
			t.Fatal("concurrent seal constructed a different gate")
		}
	}
}
