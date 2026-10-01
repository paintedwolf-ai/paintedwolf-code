package filebriefing

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryKeepsReusableBriefingsForExactPresentations(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	first := Briefing{ProjectID: "p", RootID: "r", Path: "main.go", TargetKey: "one", Presentation: "current", SourceSHA256: "one", Trigger: "manual", Status: StatusPending, UpdatedAt: time.Now()}
	got, err := store.Start(ctx, first)
	testutil.FailErr(t, "start first briefing", err)
	if got.SourceSHA256 != "one" {
		t.Fatalf("first start = %#v", got)
	}
	testutil.FailErr(t, "complete first briefing", store.Complete(ctx, Outcome{
		ProjectID: "p", RootID: "r", Path: "main.go", TargetKey: "one", AttemptID: got.AttemptID,
		Sections: []Section{{Kind: "purpose", Text: "first explanation"}}, UpdatedAt: time.Now(),
	}))
	got, err = store.Start(ctx, first)
	testutil.FailErr(t, "reuse first briefing", err)
	if got.Status != StatusComplete || len(got.Sections) != 1 || got.Sections[0].Text != "first explanation" {
		t.Fatalf("same revision = %#v", got)
	}
	second := first
	second.TargetKey = "two"
	second.SourceSHA256 = "two"
	second.Status = StatusPending
	got, err = store.Start(ctx, second)
	testutil.FailErr(t, "start second briefing", err)
	if got.Status != StatusPending || len(got.Sections) != 0 {
		t.Fatalf("new revision = %#v", got)
	}
}

func TestMemoryClearRemovesPendingAndCompletedBriefings(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	var completeAttemptID string
	for _, target := range []string{"pending", "complete"} {
		started, err := store.Start(ctx, Briefing{
			ProjectID: "p", RootID: "r", Path: "main.go", TargetKey: target,
			Presentation: "current", SourceSHA256: target, Trigger: "manual",
			Status: StatusPending, UpdatedAt: time.Now(),
		})
		testutil.FailErr(t, "start "+target, err)
		if target == "complete" {
			completeAttemptID = started.AttemptID
		}
	}
	testutil.FailErr(t, "complete briefing", store.Complete(ctx, Outcome{
		ProjectID: "p", RootID: "r", Path: "main.go", TargetKey: "complete",
		AttemptID: completeAttemptID, UpdatedAt: time.Now(),
	}))
	testutil.FailErr(t, "clear briefings", store.Clear(ctx))
	for _, target := range []string{"pending", "complete"} {
		if _, err := store.Get(ctx, "p", "r", "main.go", target); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s briefing error = %v, want ErrNotFound", target, err)
		}
	}
}

func TestMaintenanceRejectsInvalidRetention(t *testing.T) {
	cfg := testConfig(t).Retention
	cfg.DeviceBytes = 0
	err := NewMemory().Maintain(t.Context(), Briefing{}, cfg)
	if err == nil {
		t.Fatal("Maintain accepted an unbounded retention configuration")
	}
}

func TestSQLClearRemovesAllBriefings(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "briefings.db"))
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	briefing := Briefing{
		ProjectID: testdbseed.DefaultProjectID, RootID: rootID, Path: "main.go", TargetKey: "one",
		Presentation: "current", SourceSHA256: "one", Trigger: "manual", Status: StatusPending,
		UpdatedAt: time.Now().UTC(),
	}
	_, err := store.Start(ctx, briefing)
	testutil.FailErr(t, "start briefing", err)
	testutil.FailErr(t, "clear briefings", store.Clear(ctx))
	if _, err := store.Get(ctx, briefing.ProjectID, briefing.RootID, briefing.Path, briefing.TargetKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cleared briefing error = %v, want ErrNotFound", err)
	}
}

func TestMemoryMaintenanceBoundsAllPresentationsAndKeepsCurrentWork(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	start := func(target string, at time.Time, pending bool) {
		t.Helper()
		started, err := store.Start(ctx, Briefing{
			ProjectID: "p", RootID: "r", Path: "main.go", TargetKey: target,
			Presentation: "current", SourceSHA256: target, Trigger: "manual",
			Status: StatusPending, UpdatedAt: at,
		})
		testutil.FailErr(t, "start "+target, err)
		if pending {
			return
		}
		testutil.FailErr(t, "complete "+target, store.Complete(ctx, Outcome{
			ProjectID: "p", RootID: "r", Path: "main.go", TargetKey: target, AttemptID: started.AttemptID,
			UpdatedAt: at,
		}))
	}
	start("pending", time.Now().UTC(), true)
	start("old", base.Add(time.Minute), false)
	start("newer", base.Add(2*time.Minute), false)
	start("newest", base.Add(3*time.Minute), false)

	cfg := testConfig(t).Retention
	cfg.RevisionsPerFile = 2
	testutil.FailErr(t, "maintain briefings", store.Maintain(ctx, Briefing{
		ProjectID: "p", RootID: "r", Path: "main.go", TargetKey: "pending",
	}, cfg))
	for _, target := range []string{"old", "newer"} {
		if _, err := store.Get(ctx, "p", "r", "main.go", target); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s briefing error = %v, want ErrNotFound", target, err)
		}
	}
	for _, target := range []string{"pending", "newest"} {
		if _, err := store.Get(ctx, "p", "r", "main.go", target); err != nil {
			t.Fatalf("retained %s: %v", target, err)
		}
	}
}

func TestSQLReusesCurrentRevisionAndRestartsFailures(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "briefings.db"))
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	first := Briefing{
		ProjectID: testdbseed.DefaultProjectID, RootID: rootID, Path: "main.go", TargetKey: "one", Presentation: "current", SourceSHA256: "one", Trigger: "manual",
		Status: StatusPending, Preview: Preview{LineCount: 4},
		UpdatedAt: time.Now().UTC(),
	}

	got, err := store.Start(ctx, first)
	testutil.FailErr(t, "first Start", err)
	if got.Status != StatusPending {
		t.Fatalf("first Start = %#v", got)
	}
	firstAttemptID := got.AttemptID
	got, err = store.Start(ctx, first)
	testutil.FailErr(t, "duplicate Start", err)
	if got.Status != StatusPending {
		t.Fatalf("duplicate Start = %#v", got)
	}

	testutil.FailErr(t, "fail briefing", store.Fail(ctx, Outcome{
		ProjectID: first.ProjectID, RootID: first.RootID, Path: first.Path, TargetKey: first.TargetKey,
		AttemptID: firstAttemptID, Error: "provider unavailable", UpdatedAt: time.Now().UTC(),
	}))
	got, err = store.Start(ctx, first)
	testutil.FailErr(t, "restart failed briefing", err)
	if got.Status != StatusPending || got.Error != "" {
		t.Fatalf("restarted briefing = %#v", got)
	}
	if got.AttemptID == firstAttemptID {
		t.Fatal("restarted briefing reused its generation attempt")
	}
	if err := store.Complete(ctx, Outcome{
		ProjectID: first.ProjectID, RootID: first.RootID, Path: first.Path, TargetKey: first.TargetKey,
		AttemptID: firstAttemptID, UpdatedAt: time.Now().UTC(),
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("superseded generation error = %v, want ErrNotFound", err)
	}

	second := first
	second.TargetKey = "two"
	second.SourceSHA256 = "two"
	second.UpdatedAt = time.Now().UTC()
	got, err = store.Start(ctx, second)
	testutil.FailErr(t, "start new revision", err)
	if got.SourceSHA256 != "two" || got.Status != StatusPending {
		t.Fatalf("new revision = %#v", got)
	}
	_, err = sqlDB.ExecContext(ctx, `UPDATE file_briefings SET preview_json = '[]' WHERE project_id = ?`, first.ProjectID)
	testutil.FailErr(t, "corrupt briefing preview", err)
	_, err = store.Get(ctx, second.ProjectID, second.RootID, second.Path, second.TargetKey)
	if err == nil || !strings.Contains(err.Error(), "preview") {
		t.Fatalf("malformed preview error = %v", err)
	}
}

func TestSQLMaintenanceOrdersFractionalTimestamps(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "briefings.db"))
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	for target, at := range map[string]time.Time{
		"whole-second": base,
		"fractional":   base.Add(900 * time.Millisecond),
	} {
		started, err := store.Start(ctx, Briefing{
			ProjectID: testdbseed.DefaultProjectID, RootID: rootID, Path: "main.go", TargetKey: target,
			Presentation: "current", SourceSHA256: target, Trigger: "manual", Status: StatusPending,
			Preview: Preview{LineCount: 1}, UpdatedAt: at,
		})
		testutil.FailErr(t, "start "+target, err)
		testutil.FailErr(t, "complete "+target, store.Complete(ctx, Outcome{
			ProjectID: testdbseed.DefaultProjectID, RootID: rootID, Path: "main.go", TargetKey: target,
			AttemptID: started.AttemptID, UpdatedAt: at,
		}))
	}
	for target, at := range map[string]time.Time{
		"whole-second": base,
		"fractional":   base.Add(900 * time.Millisecond),
	} {
		_, err := sqlDB.ExecContext(ctx, `UPDATE file_briefings SET last_accessed_at_ms = ? WHERE target_key = ?`, at.UnixMilli(), target)
		testutil.FailErr(t, "set access time "+target, err)
	}

	cfg := testConfig(t).Retention
	cfg.RevisionsPerFile = 1
	testutil.FailErr(t, "maintain briefings", store.Maintain(ctx, Briefing{
		ProjectID: testdbseed.DefaultProjectID, RootID: rootID, Path: "main.go", TargetKey: "fractional",
	}, cfg))
	if _, err := store.Get(ctx, testdbseed.DefaultProjectID, rootID, "main.go", "whole-second"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("whole-second briefing error = %v, want ErrNotFound", err)
	}
	if _, err := store.Get(ctx, testdbseed.DefaultProjectID, rootID, "main.go", "fractional"); err != nil {
		t.Fatalf("fractional briefing: %v", err)
	}
}

func TestSQLMaintenanceUsesByteBudgetAndRecentAccess(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "briefing-budget.db"))
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	for index, target := range []string{"old", "new"} {
		at := base.Add(time.Duration(index) * time.Minute)
		started, err := store.Start(ctx, Briefing{
			ProjectID: testdbseed.DefaultProjectID, RootID: rootID, Path: target + ".go", TargetKey: target,
			Presentation: "current", SourceSHA256: target, Trigger: "manual", Status: StatusPending,
			Preview: Preview{LineCount: 10}, UpdatedAt: at, LastAccessedAt: at,
		})
		testutil.FailErr(t, "start "+target, err)
		testutil.FailErr(t, "complete "+target, store.Complete(ctx, Outcome{
			ProjectID: testdbseed.DefaultProjectID, RootID: rootID, Path: target + ".go", TargetKey: target,
			AttemptID: started.AttemptID,
			Sections:  []Section{{Kind: "purpose", Text: strings.Repeat(target, 40)}}, UpdatedAt: at,
		}))
		_, err = sqlDB.ExecContext(ctx, `UPDATE file_briefings SET last_accessed_at_ms = ? WHERE target_key = ?`, at.UnixMilli(), target)
		testutil.FailErr(t, "set access time "+target, err)
	}

	_, err := store.Get(ctx, testdbseed.DefaultProjectID, rootID, "old.go", "old")
	testutil.FailErr(t, "touch old briefing", err)
	var budget int64
	testutil.FailErr(t, "read byte budget", sqlDB.QueryRowContext(ctx,
		`SELECT MAX(storage_bytes) FROM file_briefings`).Scan(&budget))
	cfg := testConfig(t).Retention
	cfg.ProjectBytes = budget
	testutil.FailErr(t, "maintain byte budget", store.Maintain(
		ctx, Briefing{ProjectID: testdbseed.DefaultProjectID, RootID: rootID, Path: "old.go", TargetKey: "old"}, cfg,
	))
	if _, err := store.Get(ctx, testdbseed.DefaultProjectID, rootID, "old.go", "old"); err != nil {
		t.Fatalf("recently used briefing was evicted: %v", err)
	}
	if _, err := store.Get(ctx, testdbseed.DefaultProjectID, rootID, "new.go", "new"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("older briefing error = %v, want ErrNotFound", err)
	}
}

func TestSQLMaintenanceCountsPendingRowsAndKeepsCurrentWork(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "briefing-pending.db"))
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	now := time.Now().UTC()
	for _, target := range []string{"abandoned-oldest", "abandoned", "running"} {
		_, err := store.Start(ctx, Briefing{
			ProjectID: testdbseed.DefaultProjectID, RootID: rootID, Path: "main.go", TargetKey: target,
			Presentation: "current", SourceSHA256: target, Trigger: "manual", Status: StatusPending,
			Preview: Preview{LineCount: 1}, UpdatedAt: now,
		})
		testutil.FailErr(t, "start "+target, err)
	}
	cfg := testConfig(t).Retention
	for index, target := range []string{"abandoned-oldest", "abandoned", "running"} {
		at := now.Add(time.Duration(index) * time.Millisecond)
		_, err := sqlDB.ExecContext(ctx, `UPDATE file_briefings SET last_accessed_at_ms = ? WHERE target_key = ?`, at.UnixMilli(), target)
		testutil.FailErr(t, "age "+target, err)
	}
	cfg.RevisionsPerFile = 1
	cfg.MaintenanceBatch = 1
	testutil.FailErr(t, "maintain pending rows", store.Maintain(
		ctx, Briefing{ProjectID: testdbseed.DefaultProjectID, RootID: rootID, Path: "main.go", TargetKey: "running"}, cfg,
	))
	for _, target := range []string{"abandoned-oldest", "abandoned"} {
		if _, err := store.Get(ctx, testdbseed.DefaultProjectID, rootID, "main.go", target); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s row error = %v, want ErrNotFound", target, err)
		}
	}
	if _, err := store.Get(ctx, testdbseed.DefaultProjectID, rootID, "main.go", "running"); err != nil {
		t.Fatalf("retained running row: %v", err)
	}
}

func TestSQLMaintenanceEnforcesDeviceRowLimitAndKeepsCurrentWork(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "briefing-device-rows.db"))
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	for index, target := range []string{"protected", "other"} {
		at := base.Add(time.Duration(index) * time.Minute)
		_, err := store.Start(ctx, Briefing{
			ProjectID: testdbseed.DefaultProjectID, RootID: rootID, Path: target + ".go", TargetKey: target,
			Presentation: "current", SourceSHA256: target, Trigger: "manual", Status: StatusPending,
			Preview: Preview{LineCount: 1}, UpdatedAt: at,
		})
		testutil.FailErr(t, "start "+target, err)
		_, err = sqlDB.ExecContext(ctx, `UPDATE file_briefings SET last_accessed_at_ms = ? WHERE target_key = ?`, at.UnixMilli(), target)
		testutil.FailErr(t, "age "+target, err)
	}

	cfg := testConfig(t).Retention
	cfg.DeviceRows = 1
	cfg.MaintenanceBatch = 1
	testutil.FailErr(t, "maintain device rows", store.Maintain(ctx, Briefing{
		ProjectID: testdbseed.DefaultProjectID, RootID: rootID, Path: "protected.go", TargetKey: "protected",
	}, cfg))
	if _, err := store.Get(ctx, testdbseed.DefaultProjectID, rootID, "protected.go", "protected"); err != nil {
		t.Fatalf("retained current work: %v", err)
	}
	if _, err := store.Get(ctx, testdbseed.DefaultProjectID, rootID, "other.go", "other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other briefing error = %v, want ErrNotFound", err)
	}
}

func TestSQLStartupMaintenanceEnforcesDeviceLimit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "briefing-startup-maintenance.db"))
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	for index, target := range []string{"old", "new"} {
		at := base.Add(time.Duration(index) * time.Minute)
		_, err := store.Start(ctx, Briefing{
			ProjectID: testdbseed.DefaultProjectID, RootID: rootID, Path: target + ".go", TargetKey: target,
			Presentation: "current", SourceSHA256: target, Trigger: "manual", Status: StatusPending,
			Preview: Preview{LineCount: 1}, UpdatedAt: at,
		})
		testutil.FailErr(t, "start "+target, err)
		_, err = sqlDB.ExecContext(ctx, `UPDATE file_briefings SET last_accessed_at_ms = ? WHERE target_key = ?`, at.UnixMilli(), target)
		testutil.FailErr(t, "age "+target, err)
	}

	cfg := testConfig(t).Retention
	cfg.DeviceRows = 1
	cfg.MaintenanceBatch = 1
	testutil.FailErr(t, "maintain startup device limit", store.MaintainDevice(ctx, cfg))
	if _, err := store.Get(ctx, testdbseed.DefaultProjectID, rootID, "old.go", "old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old briefing error = %v, want ErrNotFound", err)
	}
	if _, err := store.Get(ctx, testdbseed.DefaultProjectID, rootID, "new.go", "new"); err != nil {
		t.Fatalf("new briefing: %v", err)
	}
}

func TestSQLLoadsSectionsAndDeclarationLocations(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "briefings.db"))
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	first := Briefing{
		ProjectID: testdbseed.DefaultProjectID, RootID: rootID, Path: "main.go", TargetKey: "one",
		Presentation: "current", SourceSHA256: "one", Trigger: "manual", Status: StatusPending,
		Preview:   Preview{LineCount: 4},
		Locations: []Location{{Line: 3, Name: "Routes", Kind: "function"}},
		UpdatedAt: time.Now().UTC(),
	}
	first, err := store.Start(ctx, first)
	testutil.FailErr(t, "start briefing", err)
	testutil.FailErr(t, "store fallback text", store.Preview(ctx, Outcome{
		ProjectID: first.ProjectID, RootID: first.RootID, Path: first.Path, TargetKey: first.TargetKey,
		AttemptID: first.AttemptID, FallbackText: "A bounded fallback.", Truncated: true, UpdatedAt: time.Now().UTC(),
	}))
	fallback, err := store.Get(ctx, first.ProjectID, first.RootID, first.Path, first.TargetKey)
	testutil.FailErr(t, "get fallback text", err)
	if fallback.FallbackText != "A bounded fallback." || !fallback.Truncated {
		t.Fatalf("fallback briefing = %+v", fallback)
	}
	first, err = store.Start(ctx, first)
	testutil.FailErr(t, "restart preview briefing", err)
	testutil.FailErr(t, "complete briefing", store.Complete(ctx, Outcome{
		ProjectID: first.ProjectID, RootID: first.RootID, Path: first.Path, TargetKey: first.TargetKey,
		AttemptID: first.AttemptID,
		Sections:  []Section{{Kind: "purpose", Text: "`Routes` 世界 requests"}}, UpdatedAt: time.Now().UTC(),
	}))

	got, err := store.Get(ctx, first.ProjectID, first.RootID, first.Path, first.TargetKey)
	testutil.FailErr(t, "get briefing", err)
	if len(got.Sections) != 1 || got.Sections[0].Text != "`Routes` 世界 requests" {
		t.Fatalf("loaded section = %#v", got.Sections)
	}
	if len(got.Locations) != 1 || got.Locations[0].Name != "Routes" || got.Locations[0].Line != 3 {
		t.Fatalf("loaded locations = %#v", got.Locations)
	}
	if got.FallbackText != "" || got.Truncated {
		t.Fatalf("completed fallback = %q truncated=%t", got.FallbackText, got.Truncated)
	}
	var stored string
	var storageBytes int64
	testutil.FailErr(t, "read sections_json", sqlDB.QueryRowContext(ctx,
		`SELECT sections_json, storage_bytes FROM file_briefings WHERE project_id = ? AND target_key = ?`,
		first.ProjectID, first.TargetKey).Scan(&stored, &storageBytes))
	if stored != "[{\"kind\":\"purpose\",\"text\":\"`Routes` 世界 requests\"}]" {
		t.Fatalf("sections_json = %s", stored)
	}
	memoryBytes, err := briefingStorageBytes(got)
	testutil.FailErr(t, "measure briefing storage", err)
	if memoryBytes != storageBytes {
		t.Fatalf("storage bytes = %d, memory accounting = %d", storageBytes, memoryBytes)
	}
}
