package security

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

// TestConformanceEffectsSettleLandedWritesBeforeMark proves a filesystem
// change that landed before mark is never charged to the read that follows.
// The watcher delivers a write through a debounce timer and a batch queue that
// WaitForBackground does not join, so a mark taken right after the write must
// wait for that pipeline to drain, or a slow read observes the source_changed
// event and the ledger commits as its own side effects.
func TestConformanceEffectsSettleLandedWritesBeforeMark(t *testing.T) {
	h := wiring.BuildForTest(t)
	hub := h.MemoryHub()
	if hub == nil {
		t.Fatal("the effects observer needs the in-memory event hub")
	}
	projectDir := h.ProjectDir(t, "effects")
	testutil.FailErr(t, "write fixture file", os.WriteFile(filepath.Join(projectDir, "README.md"), []byte("effects\n"), 0o644))
	createTestProjectHTTP(t, h.Server, projectDir)
	settleSourceWatcher(t, h.Server.WaitForBackground, hub, projectDir)
	ctx := t.Context()
	published, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to source changes", err)
	defer unsubscribe()
	effects := openConformanceEffects(t, h, hub, projectDir)

	const landed = "landed.txt"
	for step := range 3 {
		testutil.FailErr(t, "write the landed file",
			os.WriteFile(filepath.Join(projectDir, landed), []byte{byte('a' + step), '\n'}, 0o644))
		mark, err := effects.mark(ctx)
		testutil.FailErr(t, "mark host state", err)
		changes, err := effects.since(ctx, mark)
		testutil.FailErr(t, "observe host state", err)
		if len(changes) > 0 {
			t.Fatalf("step %d: a write that landed before mark was charged to the read: %v", step, changes)
		}
	}
	if !sourceChangeNamed(published, landed) {
		t.Fatal("the watcher never reported the landed file, so the settle was not exercised")
	}
}

// sourceChangeNamed reports whether an already delivered source_changed event
// names path, so a passing settle proves draining rather than a blind watcher.
func sourceChangeNamed(published <-chan wire.EventEnvelope, path string) bool {
	for {
		select {
		case envelope := <-published:
			var changed wire.SourceChangesEvent
			if envelope.Topic != wire.EventTopicSourceChanged || json.Unmarshal(envelope.Data, &changed) != nil {
				continue
			}
			if slices.ContainsFunc(changed.Changes, func(c wire.SourceChange) bool { return c.Path == path }) {
				return true
			}
		default:
			return false
		}
	}
}
