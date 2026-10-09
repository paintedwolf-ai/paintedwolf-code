package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/progress"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Queued prompts belong to the discarded timeline.
func TestRewindClearsTheNextTurnQueue(t *testing.T) {
	mgr, sessionID, _ := newCheckpointTestSession(t)
	ctx := context.Background()

	_, err := mgr.Submissions.Prompt(ctx, sessionID, "the ask")
	testutil.FailErr(t, "prompt", err)
	anchor := visibleUserMessageIDs(t, mgr, sessionID)[0]

	mgr.queue.AppendOrdered(sessionID, "", testutil.HostOwner().ID, "queued follow-up", 0, time.Time{})
	if got := mgr.Drafts.Snapshot(sessionID); len(got.QueueItems) != 1 {
		t.Fatalf("queue setup failed: %d items", len(got.QueueItems))
	}

	operationID := uuid.NewString()
	first, err := rewindTest(t, mgr, ctx, operationID, sessionID, anchor)
	testutil.FailErr(t, "rewind", err)

	if got := mgr.Drafts.Snapshot(sessionID); len(got.QueueItems) != 0 {
		t.Fatalf("queue still holds %d item(s) after rewind", len(got.QueueItems))
	}
	replayed, err := rewindTest(t, mgr, ctx, operationID, sessionID, anchor)
	testutil.FailErr(t, "replay rewind", err)
	if !reflect.DeepEqual(replayed, first) {
		t.Fatalf("rewind replay = %+v, want exact %+v", replayed, first)
	}
	if _, err := rewindTest(t, mgr, ctx, operationID, sessionID, uuid.NewString()); !errors.Is(err, checkpointcontrol.ErrRewindOperationConflict) {
		t.Fatalf("rewind operation reuse = %v, want checkpointcontrol.ErrRewindOperationConflict", err)
	}
}

// Removing the open anchor resets pre-image capture for the next prompt.
func TestRewindClosesTheCaptureInterval(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	ctx := context.Background()

	_, err := mgr.Submissions.Prompt(ctx, sessionID, "the ask")
	testutil.FailErr(t, "prompt", err)
	anchor := visibleUserMessageIDs(t, mgr, sessionID)[0]
	mgr.Captures.RecordPrimaryMutation(ctx, sessionID, "foo.go")

	_, err = rewindTest(t, mgr, ctx, uuid.NewString(), sessionID, anchor)
	testutil.FailErr(t, "rewind", err)

	checkpoint := sessioncheckpoint.New(mgr.dataDir, dir, mgr.store)
	mgr.Captures.RecordPrimaryMutation(ctx, sessionID, "foo.go")
	if _, err := checkpoint.Load(ctx, sessionID, anchor); !errors.Is(err, sessionstore.ErrCheckpointMissing) {
		t.Fatalf("capture recreated the rewound anchor: %v", err)
	}
	_, err = mgr.Submissions.Prompt(ctx, sessionID, "the next ask")
	testutil.FailErr(t, "start next capture interval", err)
	nextAnchor := visibleUserMessageIDs(t, mgr, sessionID)[0]
	mgr.Captures.RecordPrimaryMutation(ctx, sessionID, "foo.go")
	captured, err := checkpoint.Load(ctx, sessionID, nextAnchor)
	testutil.FailErr(t, "load next capture interval", err)
	if _, ok := captured.Paths["foo.go"]; !ok {
		t.Fatal("the next turn skipped the path's pre-image")
	}

}

// Rewind restores project decisions and the format marker published with them.
func TestRewindUndoesTurnChangesToTheProjectOverlay(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	ctx := context.Background()
	blueprint := settingsoverlay.DirName() + "/blueprints/feature.md"
	ignores := settingsoverlay.DirName() + "/ignores.yaml"
	formatMarker := settingsoverlay.Rel(settingsoverlay.FormatFileName)
	abs := func(rel string) string { return filepath.Join(dir, filepath.FromSlash(rel)) }
	testutil.FailErr(t, "mkdir blueprints", os.MkdirAll(filepath.Dir(abs(blueprint)), 0o755))
	testutil.FailErr(t, "seed blueprint", os.WriteFile(abs(blueprint), []byte("# original plan\n"), 0o644))

	_, err := mgr.Submissions.Prompt(ctx, sessionID, "rewrite the plan and quiet that finding")
	testutil.FailErr(t, "prompt", err)
	anchor := visibleUserMessageIDs(t, mgr, sessionID)[0]

	mgr.Captures.RecordPrimaryMutation(ctx, sessionID, blueprint)
	testutil.FailErr(t, "turn rewrites the plan", os.WriteFile(abs(blueprint), []byte("# agent plan\n"), 0o644))
	mgr.Captures.RecordPrimaryMutation(ctx, sessionID, formatMarker)
	testutil.FailErr(t, "publish ignore format", settingsoverlay.EnsureCurrentFormat(dir))
	mgr.Captures.RecordPrimaryMutation(ctx, sessionID, ignores)
	testutil.FailErr(t, "turn accepts a finding",
		os.WriteFile(abs(ignores), []byte("version: 1\nfindings:\n  - path: test/**\n    reason: reviewed fixture\n"), 0o644))
	testutil.FailErr(t, "validate current overlay", settingsoverlay.CheckFormat(dir))
	recordRewindTestEffect(t, mgr, sessionID, blueprint, []byte("# original plan\n"), []byte("# agent plan\n"), api.SourceChangeOpWrite)
	for _, rel := range []string{formatMarker, ignores} {
		raw, err := os.ReadFile(abs(rel))
		testutil.FailErr(t, "read overlay", err)
		recordRewindTestEffect(t, mgr, sessionID, rel, nil, raw, api.SourceChangeOpCreate)
	}

	result, err := rewindTest(t, mgr, ctx, uuid.NewString(), sessionID, anchor)
	testutil.FailErr(t, "rewind", err)

	got, err := os.ReadFile(abs(blueprint))
	testutil.FailErr(t, "read blueprint", err)
	if string(got) != "# original plan\n" {
		t.Fatalf("blueprint = %q, want the pre-turn plan restored", got)
	}
	if _, err := os.Stat(abs(ignores)); !os.IsNotExist(err) {
		t.Fatal("a risk acceptance the rewound turn added is still standing")
	}
	if _, err := os.Stat(abs(formatMarker)); !os.IsNotExist(err) {
		t.Fatal("the rewound turn's format marker remains")
	}
	format, err := settingsoverlay.ReadFormat(dir)
	testutil.FailErr(t, "read restored format", err)
	if format != 1 {
		t.Fatalf("restored overlay format = %d, want 1", format)
	}
	if len(result.RestoredPaths) != 3 {
		t.Fatalf("RestoredPaths = %v, want all three overlay paths", result.RestoredPaths)
	}
}

// Rewind and active writes are mutually exclusive.
func TestRewindRefusesWhileBusy(t *testing.T) {
	mgr, sessionID, _ := newCheckpointTestSession(t)
	ctx := context.Background()

	_, err := mgr.Submissions.Prompt(ctx, sessionID, "the ask")
	testutil.FailErr(t, "prompt", err)
	anchor := visibleUserMessageIDs(t, mgr, sessionID)[0]

	testutil.FailErr(t, "mark busy", mgr.store.SetSessionStatus(ctx, sessionID, api.SessionStatusBusy))
	_, err = rewindTest(t, mgr, ctx, uuid.NewString(), sessionID, anchor)
	if err == nil {
		t.Fatal("rewind succeeded on a busy session")
	}
	if !errors.Is(err, checkpointcontrol.ErrSessionNotIdle) {
		t.Fatalf("err = %v, want checkpointcontrol.ErrSessionNotIdle", err)
	}
}

// Rewind clears separately persisted progress so deleted commitments stay out of the next prompt.
func TestRewindClearsTheProgressChecklist(t *testing.T) {
	mgr, sessionID, _ := newCheckpointTestSession(t)
	mgr.SetProgressStore(progress.NewMemoryStore())
	ctx := context.Background()

	_, err := mgr.Submissions.Prompt(ctx, sessionID, "the ask")
	testutil.FailErr(t, "prompt", err)
	anchor := visibleUserMessageIDs(t, mgr, sessionID)[0]

	mgr.progress.Set(sessionID, "## Progress\n- [x] ship the thing\n")
	if mgr.progress.Get(t.Context(), sessionID) == "" {
		t.Fatal("progress setup failed")
	}

	_, err = rewindTest(t, mgr, ctx, uuid.NewString(), sessionID, anchor)
	testutil.FailErr(t, "rewind", err)

	if got := mgr.progress.Get(t.Context(), sessionID); got != "" {
		t.Fatalf("progress = %q, want cleared — it described work the rewind deleted", got)
	}
}
