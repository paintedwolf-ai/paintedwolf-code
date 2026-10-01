package kick

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubEngine captures kick renders without reading template files.
type stubEngine struct {
	render   func(name string, data map[string]any) (string, error)
	lastName string
	lastData map[string]any
}

func TestRenderPendingNudgeRetainsKickAcrossRenderFailure(t *testing.T) {
	failed := true
	engine := &stubEngine{render: func(name string, _ map[string]any) (string, error) {
		if failed {
			return "", errors.New("temporary render failure")
		}
		return "rendered:" + name, nil
	}}
	kicks := &KickEngine{}
	kicks.SetPromptEngine(engine)
	kicks.QueueDeferred("s1", "coordinator-gate-blocked")
	if id := kicks.TakePendingKickID("s1"); id != "coordinator-gate-blocked" {
		t.Fatalf("staged id = %q", id)
	}
	if _, _, ok, err := kicks.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{}); ok || err == nil {
		t.Fatalf("failed render = ok %v, err %v", ok, err)
	}
	if id, ok := kicks.PeekPendingKickID("s1"); !ok || id != "coordinator-gate-blocked" {
		t.Fatalf("failed render consumed kick: %q, %v", id, ok)
	}
	failed = false
	text, lease, ok, err := kicks.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{})
	if err != nil || !ok || text != "rendered:coordinator-gate-blocked" {
		t.Fatalf("retry = %q, %v, %v", text, ok, err)
	}
	kicks.AckPendingNudge("s1", lease)
	if _, ok := kicks.PeekPendingKickID("s1"); ok {
		t.Fatal("acknowledged kick remained pending")
	}
}

func TestAckPendingNudgeDoesNotConsumeReplacement(t *testing.T) {
	kicks := &KickEngine{}
	kicks.QueuePendingText("s1", "first", "kick-1")
	if id := kicks.TakePendingKickID("s1"); id != "kick-1" {
		t.Fatalf("staged id = %q", id)
	}
	_, lease, ok, err := kicks.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{})
	if err != nil || !ok {
		t.Fatalf("render = ok %v, err %v", ok, err)
	}
	kicks.DropKickID("s1", "kick-1")
	kicks.QueueEager("s1", "kick-2", nil)

	kicks.AckPendingNudge("s1", lease)
	if id, ok := kicks.PeekPendingKickID("s1"); !ok || id != "kick-2" {
		t.Fatalf("replacement consumed as %q,%v", id, ok)
	}
}

func (s *stubEngine) Render(context.Context, string, map[string]any) (string, error) {
	return "", nil
}

func (s *stubEngine) RenderInject(context.Context, string, map[string]any) (string, error) {
	return "", nil
}

func (s *stubEngine) RenderGuidance(context.Context, string, map[string]any) (string, error) {
	return "", nil
}

func (s *stubEngine) RenderKick(_ context.Context, name string, data map[string]any) (string, error) {
	s.lastName = name
	s.lastData = data
	if s.render != nil {
		return s.render(name, data)
	}
	return "rendered:" + name, nil
}

func (s *stubEngine) Register(string, string) error { return nil }

func (s *stubEngine) LoadFromDir(context.Context, string) error { return nil }

func TestFormatKickRelativeAgo(t *testing.T) {
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{"just now", now.Add(-30 * time.Second), "just now"},
		{"minutes", now.Add(-5 * time.Minute), "5m ago"},
		{"hours", now.Add(-3 * time.Hour), "3h ago"},
		{"days", now.Add(-50 * time.Hour), "2d ago"},
		{"future clamps to absolute", now.Add(2 * time.Minute), "2m ago"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatKickRelativeAgo(now, tc.t); got != tc.want {
				t.Errorf("FormatKickRelativeAgo = %q want %q", got, tc.want)
			}
		})
	}
}

func TestKickOptionsPopulateMeta(t *testing.T) {
	completed := time.Date(2026, 6, 14, 11, 0, 0, 0, time.FixedZone("x", 3600))
	var m kickMeta
	opts := []KickOption{
		WithLegCompletedAt(completed),
		WithTopologyOutput("  topo out  "),
		WithWorkerDigest("  digest  "),
		WithPendingOverlayPromote([]string{"job-a", "job-b"}),
		WithPartialWorkerJobs([]string{"job-partial"}),
		WithScanFinished("  scan-1  ", "  complete  ", "  sast  ", 7),
		WithPromotedPaths([]string{" x.go ", "", "  ", "y.go"}),
	}
	for _, opt := range opts {
		opt(&m)
	}

	if m.completedAt == nil || !m.completedAt.Equal(completed.UTC()) {
		t.Errorf("completedAt = %v want %v (UTC normalized)", m.completedAt, completed.UTC())
	}
	if m.completedAt.Location() != time.UTC {
		t.Errorf("completedAt not normalized to UTC: %v", m.completedAt.Location())
	}
	if m.topologyOutput != "topo out" {
		t.Errorf("topologyOutput = %q want trimmed", m.topologyOutput)
	}
	if m.workerDigest != "digest" {
		t.Errorf("workerDigest = %q want trimmed", m.workerDigest)
	}
	if len(m.pendingOverlayPromoteJobs) != 2 {
		t.Errorf("pendingOverlayPromoteJobs = %v", m.pendingOverlayPromoteJobs)
	}
	if len(m.partialWorkerJobs) != 1 || m.partialWorkerJobs[0] != "job-partial" {
		t.Errorf("partialWorkerJobs = %v", m.partialWorkerJobs)
	}
	if m.scanID != "scan-1" || m.scanStatus != "complete" || m.scanCategories != "sast" || m.scanFindingsCount != 7 {
		t.Errorf("scan meta = %+v", m)
	}
	wantPaths := []string{"x.go", "y.go"}
	if len(m.promotedPaths) != len(wantPaths) {
		t.Fatalf("promotedPaths = %v want %v (empties skipped, trimmed)", m.promotedPaths, wantPaths)
	}
	for i, p := range wantPaths {
		if m.promotedPaths[i] != p {
			t.Errorf("promotedPaths[%d] = %q want %q", i, m.promotedPaths[i], p)
		}
	}
}

func TestWithLegCompletedAtZeroIsNoop(t *testing.T) {
	var m kickMeta
	WithLegCompletedAt(time.Time{})(&m)
	if m.completedAt != nil {
		t.Errorf("zero time should not set completedAt, got %v", m.completedAt)
	}
}

func TestBuildKickRenderDataPreservesCapturedPromotedPaths(t *testing.T) {
	meta := kickMeta{}
	WithPromotedPaths([]string{"captured.go"})(&meta)
	data := buildKickRenderData(meta, CoordinatorKickRenderContext{PromotedPaths: []string{"live.go"}}, time.Now().UTC())
	paths, ok := data["promoted_paths"].([]string)
	if !ok || len(paths) != 1 || paths[0] != "captured.go" {
		t.Fatalf("promoted paths = %#v, want captured event paths", data["promoted_paths"])
	}
}

func TestWithPendingOverlayPromoteCopiesSlice(t *testing.T) {
	src := []string{"job-a"}
	var m kickMeta
	WithPendingOverlayPromote(src)(&m)
	src[0] = "mutated"
	if m.pendingOverlayPromoteJobs[0] != "job-a" {
		t.Errorf("option retained alias to caller slice: %v", m.pendingOverlayPromoteJobs)
	}
}

func TestNilEngineMethodsAreSafe(t *testing.T) {
	var k *KickEngine
	k.SetPromptEngine(&stubEngine{})
	k.QueuePendingText("s", "t", "id")
	k.QueueDeferred("s", "coordinator-gate-blocked")
	k.QueueEager("s", "worker-task-started", nil)
	k.ClearPending("s")
	if id, ok := k.PeekPendingKickID("s"); id != "" || ok {
		t.Errorf("nil PeekPendingKickID = %q,%v", id, ok)
	}
	if id := k.TakePendingKickID("s"); id != "" {
		t.Errorf("nil TakePendingKickID = %q", id)
	}
	if n, _, ok, _ := k.RenderPendingNudge(t.Context(), "s", CoordinatorKickRenderContext{}); n != "" || ok {
		t.Errorf("nil RenderPendingNudge = %q,%v", n, ok)
	}
	if cfg := k.config(); cfg.engine != nil {
		t.Errorf("nil config = %+v", cfg)
	}
}

func TestQueuePendingTextWithKickID(t *testing.T) {
	k := &KickEngine{}
	k.QueuePendingText("s1", "  do the thing  ", "  kick-7  ")

	if id, ok := k.PeekPendingKickID("s1"); !ok || id != "kick-7" {
		t.Fatalf("PeekPendingKickID = %q,%v want kick-7", id, ok)
	}
	if id := k.TakePendingKickID("s1"); id != "kick-7" {
		t.Fatalf("TakePendingKickID = %q want kick-7", id)
	}
	if id, ok := k.PeekPendingKickID("s1"); !ok || id != "kick-7" {
		t.Errorf("staged PeekPendingKickID = %q,%v want kick-7", id, ok)
	}
	n, _, ok, _ := k.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{})
	if !ok || n != "do the thing" {
		t.Errorf("RenderPendingNudge = %q,%v want trimmed nudge", n, ok)
	}
}

func TestQueuePendingTextRequiresKickID(t *testing.T) {
	k := &KickEngine{}
	k.QueuePendingText("s1", "  bare nudge  ", "   ")
	if id, ok := k.PeekPendingKickID("s1"); ok || id != "" {
		t.Errorf("id-less nudge queued as %q,%v", id, ok)
	}
	if n, _, ok, _ := k.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{}); ok || n != "" {
		t.Errorf("id-less nudge rendered as %q,%v", n, ok)
	}
}

func TestRenderPendingNudgeDropsStaleBatchSeq(t *testing.T) {
	eng := &stubEngine{}
	k := &KickEngine{}
	k.SetPromptEngine(eng)
	k.QueueDeferred("s1", "coordinator-scheduled", WithBatchSeq(2))
	if id := k.TakePendingKickID("s1"); id != "coordinator-scheduled" {
		t.Fatalf("TakePendingKickID = %q want %s", id, "coordinator-scheduled")
	}
	if _, _, ok, _ := k.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{BatchSeq: 3}); ok {
		t.Fatal("stale batch_seq kick should drop at render")
	}
	if eng.lastName != "" {
		t.Fatalf("stale kick should not render, got %q", eng.lastName)
	}
	k.QueueDeferred("s1", "coordinator-scheduled", WithBatchSeq(3))
	if id := k.TakePendingKickID("s1"); id != "coordinator-scheduled" {
		t.Fatalf("TakePendingKickID = %q", id)
	}
	if _, _, ok, _ := k.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{BatchSeq: 3}); !ok {
		t.Fatal("live batch_seq kick should render")
	}
}

func TestQueuePendingTextIgnoresBlankInput(t *testing.T) {
	k := &KickEngine{}
	k.QueuePendingText("  ", "text", "id")
	k.QueuePendingText("s1", "   ", "id")
	if _, _, ok, _ := k.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{}); ok {
		t.Errorf("blank session/text should not queue anything")
	}
}

func TestQueueCoordinatorKickWithoutEngineReturnsRetryableError(t *testing.T) {
	k := &KickEngine{}
	k.QueueDeferred("s1", "coordinator-gate-blocked")
	if id, ok := k.PeekPendingKickID("s1"); !ok || id != "coordinator-gate-blocked" {
		t.Errorf("without engine kick should still queue, got %q,%v", id, ok)
	}
	if id := k.TakePendingKickID("s1"); id != "coordinator-gate-blocked" {
		t.Fatalf("TakePendingKickID = %q want %s", id, "coordinator-gate-blocked")
	}
	if _, _, ok, err := k.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{}); ok || err == nil {
		t.Errorf("RenderPendingNudge = ok %v, error %v", ok, err)
	}
	if id, ok := k.PeekPendingKickID("s1"); !ok || id != "coordinator-gate-blocked" {
		t.Fatalf("pending kick after render failure = (%q, %v)", id, ok)
	}
}

func TestQueueCoordinatorKickRendersAtTake(t *testing.T) {
	eng := &stubEngine{}
	k := &KickEngine{}
	k.SetPromptEngine(eng)

	completed := time.Now().UTC()
	k.QueueDeferred("s1", "coordinator-scan-finished",
		WithLegCompletedAt(completed),
		WithWorkerDigest("digest"),
		WithTopologyOutput("topo"),
		WithPendingOverlayPromote([]string{"stale-job"}),
		WithPartialWorkerJobs([]string{"stale-partial"}),
		WithPromotedPaths([]string{"stale.go"}),
		WithScanFinished("scan-1", "complete", "sast", 3),
		nil,
	)
	if eng.lastName != "" {
		t.Fatalf("RenderKick at queue time = %q want deferred", eng.lastName)
	}

	if id := k.TakePendingKickID("s1"); id != "coordinator-scan-finished" {
		t.Fatalf("TakePendingKickID = %q want %s", id, "coordinator-scan-finished")
	}
	live := CoordinatorKickRenderContext{
		PendingOverlayJobs: []string{"job-a"},
		PartialWorkerJobs:  []string{"job-p"},
		PromotedPaths:      []string{"x.go"},
	}
	n, _, ok, _ := k.RenderPendingNudge(t.Context(), "s1", live)
	if !ok || n != "rendered:coordinator-scan-finished" {
		t.Errorf("RenderPendingNudge = %q,%v", n, ok)
	}
	if eng.lastName != "coordinator-scan-finished" {
		t.Errorf("RenderKick name = %q want coordinator-scan-finished", eng.lastName)
	}
	for _, key := range []string{"completed_ago", "worker_digest", "topology_output", "pending_overlay_jobs", "partial_worker_jobs", "promoted_paths", "scan_id", "scan_status", "scan_categories", "scan_findings_count"} {
		if _, ok := eng.lastData[key]; !ok {
			t.Errorf("render data missing %q: %+v", key, eng.lastData)
		}
	}
	if got, _ := eng.lastData["pending_overlay_jobs"].([]string); len(got) != 1 || got[0] != "job-a" {
		t.Errorf("pending_overlay_jobs = %v want live job-a not queued stale-job", eng.lastData["pending_overlay_jobs"])
	}
}

func TestQueueCoordinatorKickEmptyRenderIsNoop(t *testing.T) {
	eng := &stubEngine{render: func(string, map[string]any) (string, error) { return "   ", nil }}
	k := &KickEngine{}
	k.SetPromptEngine(eng)
	k.QueueDeferred("s1", "coordinator-gate-blocked")
	if id, ok := k.PeekPendingKickID("s1"); !ok || id != "coordinator-gate-blocked" {
		t.Errorf("deferred kick should queue before render, got %q,%v", id, ok)
	}
	if id := k.TakePendingKickID("s1"); id != "coordinator-gate-blocked" {
		t.Fatalf("TakePendingKickID = %q", id)
	}
	if _, _, ok, _ := k.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{}); ok {
		t.Errorf("empty render should not produce nudge")
	}
}

func TestQueueEagerRequiresInformRender(t *testing.T) {
	eng := &stubEngine{}
	k := &KickEngine{}
	k.SetPromptEngine(eng)

	k.QueueEager("s1", "   ", map[string]string{"task": "scan"})
	if eng.lastName != "" {
		t.Errorf("empty template must not render, got %q", eng.lastName)
	}
	if _, _, ok, _ := k.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{}); ok {
		t.Errorf("empty template must not queue nudge")
	}

	k.QueueEager("s1", "worker-task-started", map[string]string{"task": "scan"})
	n, _, ok, _ := k.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{})
	if !ok || n != "rendered:"+"worker-task-started" {
		t.Errorf("RenderPendingNudge = %q,%v", n, ok)
	}
	if got, _ := eng.lastData["task"].(string); got != "scan" {
		t.Errorf("worker data not forwarded: %+v", eng.lastData)
	}
}

func TestQueueEagerWithoutEngineReturnsRetryableError(t *testing.T) {
	k := &KickEngine{}
	k.QueueEager("s1", "worker-closeout", nil)
	if _, _, ok, err := k.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{}); ok || err == nil {
		t.Fatalf("unwired eager kick = ok %v, err %v", ok, err)
	}
	if id, ok := k.PeekPendingKickID("s1"); !ok || id != "worker-closeout" {
		t.Fatalf("failed eager kick was consumed: %q, %v", id, ok)
	}
}

func TestClearPendingDropsEverything(t *testing.T) {
	k := &KickEngine{}
	k.QueuePendingText("s1", "queued", "kick-1")

	k.ClearPending("s1")
	if id, ok := k.PeekPendingKickID("s1"); ok || id != "" {
		t.Errorf("ClearPending left kick id %q,%v", id, ok)
	}
	if _, _, ok, _ := k.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{}); ok {
		t.Errorf("ClearPending left a pending nudge")
	}
}

func TestSessionKickQueueDedupAndFIFO(t *testing.T) {
	k := &KickEngine{}
	q := k.sessionKickQueue("s1")
	q.push(kickQueueItem{kickID: "a", deferred: true})
	q.push(kickQueueItem{kickID: "a", deferred: true})
	q.push(kickQueueItem{kickID: "b", deferred: true})
	q.push(kickQueueItem{kickID: "", deferred: true})
	q.push(kickQueueItem{kickID: "c", nudge: ""})

	if item, ok := q.peek(); !ok || item.kickID != "a" {
		t.Fatalf("peek = %+v,%v want first push", item, ok)
	}

	if id := k.TakePendingKickID("s1"); id != "a" {
		t.Fatalf("first take = %q", id)
	}
	first := q.stagedItem()
	if first == nil {
		t.Fatal("first kick was not staged")
	}
	k.AckPendingNudge("s1", first.lease)
	if id := k.TakePendingKickID("s1"); id != "b" {
		t.Fatalf("second take = %q", id)
	}
	second := q.stagedItem()
	if second == nil {
		t.Fatal("second kick was not staged")
	}
	k.AckPendingNudge("s1", second.lease)
	if id := k.TakePendingKickID("s1"); id != "" {
		t.Errorf("queue retained %q", id)
	}
}

func TestQueueDeferredLatestReplacesStagedLifecycleGuidance(t *testing.T) {
	engine := &stubEngine{render: func(name string, _ map[string]any) (string, error) { return name, nil }}
	k := &KickEngine{}
	k.SetPromptEngine(engine)
	k.QueueDeferredLatest("s1", "phase.entered", "phase-old")
	if got := k.TakePendingKickID("s1"); got != "phase-old" {
		t.Fatalf("staged kick = %q want phase-old", got)
	}
	k.QueueDeferredLatest("s1", "phase.entered", "phase-current")
	text, lease, ok, err := k.RenderPendingNudge(t.Context(), "s1", CoordinatorKickRenderContext{})
	if err != nil {
		t.Fatalf("render latest phase: %v", err)
	}
	if !ok || text != "phase-current" {
		t.Fatalf("latest phase render = %q,%v", text, ok)
	}
	k.AckPendingNudge("s1", lease)
	if _, found := k.PeekPendingKickID("s1"); found {
		t.Fatal("superseded phase guidance remained queued")
	}
}

func TestSessionKickQueueClearAndEmptyPeek(t *testing.T) {
	var q sessionKickQueue
	if _, ok := q.peek(); ok {
		t.Errorf("empty peek should be false")
	}
	q.push(kickQueueItem{kickID: "a", deferred: true})
	q.clear()
	if _, ok := q.peek(); ok {
		t.Errorf("peek after clear should be false")
	}
}

func TestDropKickIDRemovesQueuedAndStaged(t *testing.T) {
	k := &KickEngine{}
	k.QueueDeferred("s1", "coordinator-feedback-pending")
	k.QueueDeferred("s1", "coordinator-feedback-received")
	k.DropKickID("s1", "coordinator-feedback-pending")
	if id := k.TakePendingKickID("s1"); id != "coordinator-feedback-received" {
		t.Fatalf("TakePendingKickID = %q want %s", id, "coordinator-feedback-received")
	}
	k.DropKickID("s1", "coordinator-feedback-received")
	if id := k.TakePendingKickID("s1"); id != "" {
		t.Fatalf("extra kick %q", id)
	}

	k.QueueDeferred("s2", "coordinator-feedback-pending")
	if id := k.TakePendingKickID("s2"); id != "coordinator-feedback-pending" {
		t.Fatalf("stage pending = %q", id)
	}
	k.DropKickID("s2", "coordinator-feedback-pending")
	if id, ok := k.PeekPendingKickID("s2"); ok {
		t.Fatalf("staged pending survived DropKickID: %q", id)
	}
	if _, _, ok, _ := k.RenderPendingNudge(t.Context(), "s2", CoordinatorKickRenderContext{}); ok {
		t.Fatal("RenderPendingNudge must not render dropped staged kick")
	}
}

func TestTakePendingKickIDUnlessSkipsStale(t *testing.T) {
	k := &KickEngine{}
	k.QueueDeferred("s1", "coordinator-feedback-pending")
	k.QueueDeferred("s1", "coordinator-feedback-received")
	id := k.TakePendingKickIDUnless("s1", func(kickID, _ string) bool {
		return kickID == "coordinator-feedback-pending"
	})
	if id != "coordinator-feedback-received" {
		t.Fatalf("TakePendingKickIDUnless = %q want %s", id, "coordinator-feedback-received")
	}
}

func TestSessionKickQueueIsPerSession(t *testing.T) {
	k := &KickEngine{}
	q1 := k.sessionKickQueue("s1")
	q2 := k.sessionKickQueue("s1")
	if q1 != q2 {
		t.Errorf("sessionKickQueue should return the same queue per session id")
	}
	if k.sessionKickQueue("s2") == q1 {
		t.Errorf("distinct sessions should get distinct queues")
	}
}

func TestDropPendingKicksForBatchSeq(t *testing.T) {
	k := &KickEngine{}
	k.sessionKickQueue("s1").push(kickQueueItem{kickID: "scheduled", deferred: true, batchSeq: 2, hasSeq: true})
	k.sessionKickQueue("s1").push(kickQueueItem{kickID: "other", deferred: true, batchSeq: 3, hasSeq: true})
	k.DropPendingKicksForBatchSeq("s1", 2)
	if id := k.TakePendingKickID("s1"); id != "other" {
		t.Fatalf("take = %q want surviving kick", id)
	}
	item := k.sessionKickQueue("s1").stagedItem()
	if item == nil {
		t.Fatal("surviving kick was not staged")
	}
	k.AckPendingNudge("s1", item.lease)
	if id := k.TakePendingKickID("s1"); id != "" {
		t.Fatal("expected only one kick after drop")
	}
}

func TestDropPendingKicksBeforeBatchSeq(t *testing.T) {
	k := &KickEngine{}
	k.sessionKickQueue("s1").push(kickQueueItem{kickID: "old", deferred: true, batchSeq: 1, hasSeq: true})
	k.sessionKickQueue("s1").push(kickQueueItem{kickID: "live", deferred: true, batchSeq: 3, hasSeq: true})
	k.DropPendingKicksBeforeBatchSeq("s1", 3)
	if id := k.TakePendingKickID("s1"); id != "live" {
		t.Fatalf("take = %q want live kick", id)
	}
}

func TestForgetSessionReleasesTheQueueEntryNotJustItsContents(t *testing.T) {
	k := &KickEngine{}
	k.QueuePendingText("s1", "nudge", "kick-id")
	if _, ok := k.kickQueues.Load("s1"); !ok {
		t.Fatal("expected a queue entry after queuing a kick")
	}
	k.ForgetSession("s1")
	if _, ok := k.kickQueues.Load("s1"); ok {
		t.Fatal("ForgetSession must delete the map entry, not just clear it")
	}
	// Contrast with ClearPending, which leaves the (now-empty) entry resident.
	k.QueuePendingText("s1", "nudge", "kick-id")
	k.ClearPending("s1")
	if _, ok := k.kickQueues.Load("s1"); !ok {
		t.Fatal("ClearPending should leave the (now empty) queue entry resident, for contrast with ForgetSession")
	}
}
