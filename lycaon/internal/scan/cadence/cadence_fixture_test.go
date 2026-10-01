package cadence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/repochange"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestCadence(t *testing.T, clock *testClock) (*Service, *scanbase.SQLStore) {
	t.Helper()
	return newTestCadenceOn(t, clock, testdbfixture.Open(t, "cadence.db"))
}

// newTestCadenceOn builds the cadence over a store the test also seeds.
func newTestCadenceOn(t *testing.T, clock *testClock, sqlDB *db.Store) (*Service, *scanbase.SQLStore) {
	t.Helper()
	store := scanbase.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	secStore, err := settings.NewSecurityScannersStoreAt(filepath.Join(t.TempDir(), "missing.yaml"))
	testutil.FailErr(t, "security scanners store", err)
	gates := scancfg.DefaultGatesConfig()
	triggers := &scanbase.TriggerService{
		Coordinator: coord,
		Registry:    defaultTestRegistry(),
		Gates:       gates,
		Settings:    secStore,
	}
	c := New(store, coord, defaultTestRegistry(), secStore, gates, triggers)
	c.Now = clock.Now
	scopeCfg, err := sourcescope.DefaultConfig()
	testutil.FailErr(t, "source scope config", err)
	c.Scopes, err = sourcescope.NewProvider(scopeCfg, nil)
	testutil.FailErr(t, "source scope provider", err)
	return c, store
}

// noteCadenceWrites delivers source invalidation before advancing the test clock.
func noteCadenceWrites(ctx context.Context, cadence *Service, projectDir string, paths []string) error {
	canonical, err := scanbase.CanonicalPath(projectDir)
	if err != nil {
		return err
	}
	repochange.Notify(ctx, repochange.Event{
		ProjectDir: canonical, Kind: repochange.WorktreeChanged, Paths: paths, Source: repochange.SourceMutation,
	})
	return cadence.NoteWrites(ctx, projectDir, paths)
}

func completeCadenceScans(t *testing.T, cadence *Service, store *scanbase.SQLStore) {
	t.Helper()
	ctx := context.Background()
	for {
		claimed, err := store.ClaimNext(ctx)
		if errors.Is(err, scanbase.ErrNoPendingScans) {
			return
		}
		testutil.FailErr(t, "claim cadence scan", err)
		won, err := store.MarkComplete(ctx, claimed, &scanoutput.Result{})
		testutil.FailErr(t, "complete cadence scan", err)
		if !won {
			t.Fatalf("scan %s completion lost", claimed.ID)
		}
		completed, err := store.Get(ctx, claimed.ID)
		testutil.FailErr(t, "read completed cadence scan", err)
		cadence.OnTerminal(ctx, *completed)
		cadence.Tick(ctx)
	}
}

type immediateTerminalCoordinator struct {
	scanbase.ScanCoordinator
	store *scanbase.SQLStore
}

func (c *immediateTerminalCoordinator) Enqueue(ctx context.Context, req scanbase.EnqueueRequest) (*api.CodeScan, error) {
	record, err := c.ScanCoordinator.Enqueue(ctx, req)
	if err != nil {
		return nil, err
	}
	claimed, err := c.store.ClaimNext(ctx)
	if err != nil {
		return nil, err
	}
	if claimed.ID != record.ID {
		return nil, fmt.Errorf("claimed scan %s, want %s", claimed.ID, record.ID)
	}
	won, err := c.store.MarkComplete(ctx, claimed, &scanoutput.Result{})
	if err != nil {
		return nil, err
	}
	if !won {
		return nil, fmt.Errorf("complete scan %s lost claim", record.ID)
	}
	return record, nil
}

type failingCoordinator struct {
	scanbase.ScanCoordinator
	mu       sync.Mutex
	failures int
}

func (f *failingCoordinator) Enqueue(ctx context.Context, req scanbase.EnqueueRequest) (*api.CodeScan, error) {
	f.mu.Lock()
	if f.failures > 0 {
		f.failures--
		f.mu.Unlock()
		return nil, fmt.Errorf("enqueue unavailable")
	}
	f.mu.Unlock()
	return f.ScanCoordinator.Enqueue(ctx, req)
}

func seedSizedProject(t *testing.T, dir string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		name := filepath.Join(dir, fmt.Sprintf("f%02d.go", i))
		if err := os.WriteFile(name, []byte("package p\n"), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
}
