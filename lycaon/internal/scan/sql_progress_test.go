package scan

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type progressOutbox struct {
	events []api.CodeScanEvent
	err    error
}

func (o *progressOutbox) EnqueueTx(_ context.Context, _ *sql.Tx, _ api.EventTopic, _ events.PublishKey, payload any) error {
	if o.err != nil {
		return o.err
	}
	o.events = append(o.events, payload.(api.CodeScanEvent))
	return nil
}

func (*progressOutbox) Notify() {}

func TestScanProgressPublishesSummaryAndEventAtomically(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "progress.db")
	root := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, root)
	store := NewSQLStore(sqlDB)
	outbox := &progressOutbox{}
	store.SetEventOutbox(outbox)
	coordinator := newTestCoordinator(t, store, nil)
	run := authorityScan("progress", "assessment", root, "snapshot", "sast", api.ScanTargetFull, nil)
	testutil.FailErr(t, "insert scan", store.Insert(t.Context(), run, nil, ""))
	claimed, err := store.ClaimNext(t.Context())
	testutil.FailErr(t, "claim scan", err)
	progress := api.ScanProgress{Chunks: 3, Completed: 1, Files: 20}
	testutil.FailErr(t, "mark progress", store.MarkProgress(t.Context(), claimed, progress))
	summary, err := coordinator.Summary(t.Context(), claimed.ID)
	testutil.FailErr(t, "read summary", err)
	if summary.Progress == nil || *summary.Progress != progress {
		t.Fatalf("summary progress = %+v, want %+v", summary.Progress, progress)
	}
	last := outbox.events[len(outbox.events)-1]
	if last.Progress == nil || *last.Progress != progress || last.Status != api.CodeScanStatusRunning {
		t.Fatalf("progress event = %+v", last)
	}
	stale := *claimed
	stale.ClaimToken = "expired-claim"
	count := len(outbox.events)
	newProgress := api.ScanProgress{Chunks: 3, Completed: 2, Files: 20}
	testutil.FailErr(t, "reject stale progress", store.MarkProgress(t.Context(), &stale, newProgress))
	if len(outbox.events) != count || *stale.Progress != progress {
		t.Fatal("stale claim published progress")
	}
	outbox.err = errors.New("outbox unavailable")
	if err := store.MarkProgress(t.Context(), claimed, newProgress); err == nil {
		t.Fatal("progress succeeded without its event")
	}
	stored, err := store.Get(t.Context(), claimed.ID)
	testutil.FailErr(t, "read rolled back scan", err)
	summary, err = coordinator.Summary(t.Context(), claimed.ID)
	testutil.FailErr(t, "read rolled back summary", err)
	if *stored.Progress != progress || *summary.Progress != progress || *claimed.Progress != progress {
		t.Fatal("failed publication advanced progress")
	}
}
