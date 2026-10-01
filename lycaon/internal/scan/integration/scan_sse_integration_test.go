//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanexecution "github.com/lycaon/lycaon/internal/scan/execution"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScanSSEIntegrationPublishesTransitions(t *testing.T) {
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	dir := testProjectDir(t)

	sqlDB := testdbfixture.Open(t, "store.db")
	// The scan path selects the subscriber's project through its attached root.
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	ch, unsubscribe, err := hub.Subscribe(context.Background(), events.Subscription{Project: testdbseed.DefaultProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsubscribe()

	outbox := eventoutbox.New(sqlDB, hub)
	outbox.Start(context.Background())
	defer func() { _ = outbox.Close() }()
	store := scan.NewSQLStore(sqlDB)
	store.SetEventOutbox(outbox)
	coord := newTestCoordinator(t, store, nil)
	mock := &scan.MockScanner{Result: &scanoutput.Result{FindingsCount: 1}}
	reg := &scan.MockRegistry{Scanner: mock}
	cfg := scancfg.DefaultRunnerConfig()
	runner := scanexecution.NewRunner(store, reg, scan.NoopIngester{}, cfg, pub)
	runner.Snapshots = coord.SnapshotStore()

	created, err := coord.Enqueue(context.Background(), scan.EnqueueRequest{
		ProjectDir: dir,
		Categories: []api.ScanCategory{api.ScanCategorySecurity},
		HeadSHA:    "sha",
	})
	testutil.FailErr(t, "coord.Enqueue failed", err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = runner.Run(ctx) }()

	var sawComplete bool
	deadline := time.After(2 * time.Second)
	for !sawComplete {
		select {
		case envelope := <-ch:
			if envelope.Topic != api.EventTopicScan {
				continue
			}
			var payload api.CodeScanEvent
			if err := json.Unmarshal(envelope.Data, &payload); err != nil {
				testutil.FailErr(t, "unmarshal JSON document", err)
			}
			if payload.ScanID != created.ID {
				continue
			}
			if payload.Status == api.CodeScanStatusComplete {
				sawComplete = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for scan SSE complete event")
		}
	}
}
