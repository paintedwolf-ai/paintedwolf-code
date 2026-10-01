package kick

import "testing"

func TestRenderLatestReachesPastTheStagedKick(t *testing.T) {
	engine := &stubEngine{render: func(name string, _ map[string]any) (string, error) { return "rendered:" + name, nil }}
	kicks := &KickEngine{}
	kicks.SetPromptEngine(engine)
	kicks.QueueDeferred("s1", "coordinator-leg-finished")
	kicks.QueueDeferredLatest("s1", "phase.entered", "coordinator-security-claims")
	if id := kicks.TakePendingKickID("s1"); id != "coordinator-leg-finished" {
		t.Fatalf("staged = %q, want the earlier kick", id)
	}
	if !kicks.HasLatest("s1", "phase.entered") {
		t.Fatal("phase guidance not held")
	}
	id, text, lease, ok, err := kicks.RenderLatest(t.Context(), "s1", "phase.entered", CoordinatorKickRenderContext{})
	if err != nil || !ok || id != "coordinator-security-claims" || text != "rendered:coordinator-security-claims" {
		t.Fatalf("render latest = %q %q %v %v", id, text, ok, err)
	}
	kicks.AckLatest("s1", "phase.entered", lease)
	if kicks.HasLatest("s1", "phase.entered") {
		t.Fatal("acknowledged phase guidance still held")
	}
	if id, ok := kicks.PeekPendingKickID("s1"); !ok || id != "coordinator-leg-finished" {
		t.Fatalf("staged kick disturbed: %q %v", id, ok)
	}
}

func TestAckLatestKeepsANewerPhase(t *testing.T) {
	engine := &stubEngine{render: func(name string, _ map[string]any) (string, error) { return name, nil }}
	kicks := &KickEngine{}
	kicks.SetPromptEngine(engine)
	kicks.QueueDeferredLatest("s1", "phase.entered", "coordinator-security-claims")
	_, _, lease, ok, err := kicks.RenderLatest(t.Context(), "s1", "phase.entered", CoordinatorKickRenderContext{})
	if err != nil || !ok {
		t.Fatalf("render = %v %v", ok, err)
	}
	kicks.QueueDeferredLatest("s1", "phase.entered", "coordinator-security-challenge")
	kicks.AckLatest("s1", "phase.entered", lease)
	id, _, _, ok, err := kicks.RenderLatest(t.Context(), "s1", "phase.entered", CoordinatorKickRenderContext{})
	if err != nil || !ok || id != "coordinator-security-challenge" {
		t.Fatalf("after ack = %q %v %v, want the newer phase kept", id, ok, err)
	}
}

func TestRenderLatestDropsGuidanceFromAnEarlierBatch(t *testing.T) {
	engine := &stubEngine{render: func(name string, _ map[string]any) (string, error) { return name, nil }}
	kicks := &KickEngine{}
	kicks.SetPromptEngine(engine)
	kicks.QueueDeferredLatest("s1", "phase.entered", "coordinator-security-claims", WithBatchSeq(1))
	if _, _, _, ok, err := kicks.RenderLatest(t.Context(), "s1", "phase.entered", CoordinatorKickRenderContext{BatchSeq: 2}); ok || err != nil {
		t.Fatalf("stale guidance rendered: %v %v", ok, err)
	}
	if kicks.HasLatest("s1", "phase.entered") {
		t.Fatal("stale guidance still held")
	}
}
