package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
)

func writePrimaryFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	testutil.FailErr(t, "mkdir primary parent", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write primary file", os.WriteFile(path, []byte(body), 0o644))
}

func newTestManager(t *testing.T) (*Manager, string, string) {
	t.Helper()
	base := t.TempDir()
	branches := filepath.Join(base, "branches")
	seeds := filepath.Join(base, "seeds")
	return NewManager(branches, seeds), branches, seeds
}

func currentSeedGeneration(t *testing.T, seedRoot, primary string) string {
	t.Helper()
	path := filepath.Join(enginepaths.ProjectSeedDir(seedRoot, primary), seedCurrentFile)
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read current seed generation", err)
	return strings.TrimSpace(string(raw))
}

func forceBridgeClone(t *testing.T, seedRoot string) {
	t.Helper()
	saved := cloneWorkspaceFile
	t.Cleanup(func() { cloneWorkspaceFile = saved })
	cloneWorkspaceFile = func(src, dst string) (bool, error) {
		rel, err := filepath.Rel(seedRoot, src)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return false, nil
		}
		body, err := os.ReadFile(src)
		if err != nil {
			return false, err
		}
		if err := os.WriteFile(dst, body, 0o600); err != nil {
			return false, err
		}
		return true, nil
	}
}

func TestSeedPersistsAcrossClaimsAndRefreshesIncrementally(t *testing.T) {
	primary := t.TempDir()
	writePrimaryFile(t, primary, "unchanged.txt", "stable")
	writePrimaryFile(t, primary, "changed.txt", "before")
	writePrimaryFile(t, primary, "deleted.txt", "remove")
	mgr, branches, seeds := newTestManager(t)
	forceBridgeClone(t, seeds)

	first, err := mgr.CreateWorkerWorkspace(context.Background(), primary, "first")
	testutil.FailErr(t, "create first worker workspace", err)
	firstGeneration := currentSeedGeneration(t, seeds, primary)

	restarted := NewManager(branches, seeds)
	second, err := restarted.CreateWorkerWorkspace(context.Background(), primary, "second")
	testutil.FailErr(t, "create second worker workspace", err)
	if got := currentSeedGeneration(t, seeds, primary); got != firstGeneration {
		t.Fatalf("unchanged source created generation %q, want reuse of %q", got, firstGeneration)
	}

	time.Sleep(time.Millisecond)
	writePrimaryFile(t, primary, "changed.txt", "after")
	testutil.FailErr(t, "remove deleted source", os.Remove(filepath.Join(primary, "deleted.txt")))
	writePrimaryFile(t, primary, "added.txt", "new")
	third, err := restarted.CreateWorkerWorkspace(context.Background(), primary, "third")
	testutil.FailErr(t, "create refreshed worker workspace", err)
	if got := currentSeedGeneration(t, seeds, primary); got == firstGeneration {
		t.Fatalf("changed source retained generation %q", got)
	}

	assertFileBody(t, filepath.Join(third.Root, "changed.txt"), "after")
	assertFileBody(t, filepath.Join(third.Root, "added.txt"), "new")
	if _, err := os.Stat(filepath.Join(third.Root, "deleted.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted source remains in refreshed workspace: %v", err)
	}
	assertFileBody(t, filepath.Join(first.Root, "changed.txt"), "before")
	assertFileBody(t, filepath.Join(second.Root, "changed.txt"), "before")
}

func TestDirectCoWDoesNotRetainSeed(t *testing.T) {
	saved := cloneWorkspaceFile
	t.Cleanup(func() { cloneWorkspaceFile = saved })
	cloneWorkspaceFile = func(src, dst string) (bool, error) {
		body, err := os.ReadFile(src)
		if err != nil {
			return false, err
		}
		return true, os.WriteFile(dst, body, 0o600)
	}
	primary := t.TempDir()
	writePrimaryFile(t, primary, "main.go", "package main")
	mgr, _, seeds := newTestManager(t)
	binding, err := mgr.CreateWorkerWorkspace(context.Background(), primary, "direct-cow")
	testutil.FailErr(t, "create direct-CoW workspace", err)
	assertFileBody(t, filepath.Join(binding.Root, "main.go"), "package main")
	if entries, err := os.ReadDir(seeds); err == nil && len(entries) != 0 {
		t.Fatalf("direct-CoW strategy retained cache entries: %v", entries)
	}
}

func TestSeedRefreshDetectsRewriteWithRestoredSizeAndModTime(t *testing.T) {
	primary := t.TempDir()
	path := filepath.Join(primary, "state.txt")
	writePrimaryFile(t, primary, "state.txt", "before")
	info, err := os.Stat(path)
	testutil.FailErr(t, "stat original source", err)
	mgr, _, seeds := newTestManager(t)
	forceBridgeClone(t, seeds)
	before, err := mgr.CreateWorkerWorkspace(context.Background(), primary, "before")
	testutil.FailErr(t, "create original workspace", err)
	assertFileBody(t, filepath.Join(before.Root, "state.txt"), "before")
	firstGeneration := currentSeedGeneration(t, seeds, primary)

	writePrimaryFile(t, primary, "state.txt", "after!")
	testutil.FailErr(t, "restore source modtime", os.Chtimes(path, info.ModTime(), info.ModTime()))
	binding, err := mgr.CreateWorkerWorkspace(context.Background(), primary, "after")
	testutil.FailErr(t, "create rewritten workspace", err)
	if got := currentSeedGeneration(t, seeds, primary); got == firstGeneration {
		t.Fatalf("same-size rewrite retained generation %q", got)
	}
	assertFileBody(t, filepath.Join(binding.Root, "state.txt"), "after!")
}

func TestSeedProvisionIsSerialized(t *testing.T) {
	primary := t.TempDir()
	for i := range 200 {
		writePrimaryFile(t, primary, filepath.Join("src", strings.Repeat("x", i%5), string(rune('a'+i%26))+".txt"), "body")
	}
	mgr, _, seeds := newTestManager(t)
	forceBridgeClone(t, seeds)

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			binding, err := mgr.CreateWorkerWorkspace(context.Background(), primary, "concurrent-"+string(rune('a'+i)))
			if err == nil {
				_, err = os.Stat(filepath.Join(binding.Root, "src"))
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent provision: %v", err)
		}
	}

	generation := currentSeedGeneration(t, seeds, primary)
	entries, err := os.ReadDir(filepath.Join(enginepaths.ProjectSeedDir(seeds, primary), seedGenerations))
	testutil.FailErr(t, "read seed generations", err)
	if len(entries) != 1 || entries[0].Name() != generation {
		t.Fatalf("generations = %v, want only %q", entries, generation)
	}
}

func TestWorkerWorkspaceIncludesLargeFile(t *testing.T) {
	primary := t.TempDir()
	path := filepath.Join(primary, "large.bin")
	file, err := os.Create(path)
	testutil.FailErr(t, "create large source", err)
	testutil.FailErr(t, "truncate large source", file.Truncate(64<<20))
	_, err = file.WriteAt([]byte("tail"), (64<<20)-4)
	testutil.FailErr(t, "write large source tail", err)
	testutil.FailErr(t, "close large source", file.Close())
	mgr, _, _ := newTestManager(t)

	binding, err := mgr.CreateWorkerWorkspace(context.Background(), primary, "large")
	testutil.FailErr(t, "create large worker workspace", err)
	info, err := os.Stat(filepath.Join(binding.Root, "large.bin"))
	testutil.FailErr(t, "stat large worker file", err)
	if info.Size() != 64<<20 {
		t.Fatalf("large file size = %d, want %d", info.Size(), int64(64<<20))
	}
}

func TestDirectCopyWhenCloneUnavailable(t *testing.T) {
	saved := cloneWorkspaceFile
	t.Cleanup(func() { cloneWorkspaceFile = saved })
	cloneWorkspaceFile = func(_, _ string) (bool, error) { return false, nil }

	primary := t.TempDir()
	writePrimaryFile(t, primary, "src/main.go", "package main")
	mgr, _, seeds := newTestManager(t)
	binding, err := mgr.CreateWorkerWorkspace(context.Background(), primary, "byte-copy")
	testutil.FailErr(t, "create byte-copy worker workspace", err)
	assertFileBody(t, filepath.Join(binding.Root, "src", "main.go"), "package main")
	if _, err := os.Stat(enginepaths.ProjectSeedDir(seeds, primary)); !os.IsNotExist(err) {
		t.Fatalf("direct-copy strategy retained a useless seed: %v", err)
	}
}

func TestWorkerWorkspaceProvisionHonorsCancellation(t *testing.T) {
	primary := t.TempDir()
	writePrimaryFile(t, primary, "main.go", "package main")
	mgr, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	binding, err := mgr.CreateWorkerWorkspace(ctx, primary, "canceled")
	if err == nil {
		t.Fatal("canceled claim succeeded")
	}
	if binding != nil {
		t.Fatalf("canceled claim returned binding %+v", binding)
	}
}

func TestRemoveSeedLeavesWorkerBranches(t *testing.T) {
	primary := t.TempDir()
	writePrimaryFile(t, primary, "main.go", "package main")
	mgr, _, seeds := newTestManager(t)
	binding, err := mgr.CreateWorkerWorkspace(context.Background(), primary, "retained")
	testutil.FailErr(t, "create worker workspace", err)

	testutil.FailErr(t, "remove seed", RemoveSeed(context.Background(), seeds, primary))
	if _, err := os.Stat(enginepaths.ProjectSeedDir(seeds, primary)); !os.IsNotExist(err) {
		t.Fatalf("seed remains after removal: %v", err)
	}
	assertFileBody(t, filepath.Join(binding.Root, "main.go"), "package main")
}

func TestBridgeReportsProgressAndCacheInventory(t *testing.T) {
	primary := t.TempDir()
	writePrimaryFile(t, primary, "src/main.go", strings.Repeat("x", 4096))
	mgr, _, seeds := newTestManager(t)
	forceBridgeClone(t, seeds)
	var reports []PreparationProgress
	ctx := WithPreparationReporter(context.Background(), func(progress PreparationProgress) {
		reports = append(reports, progress)
	})
	_, err := mgr.CreateWorkerWorkspace(ctx, primary, "progress")
	testutil.FailErr(t, "create bridge workspace", err)
	if len(reports) == 0 {
		t.Fatal("bridge workspace emitted no preparation progress")
	}
	var last PreparationProgress
	for _, p := range reports {
		if p.TotalBytes > 0 {
			last = p
		}
	}
	if last.Strategy != ProvisionBridgeCoW || last.TotalBytes != 4096 {
		t.Fatalf("provision progress = %+v (all=%+v)", last, reports)
	}
	caches, err := ListSeedCaches(context.Background(), seeds)
	testutil.FailErr(t, "list seed caches", err)
	if len(caches) != 1 || caches[0].SourceRoot != primary || caches[0].LogicalBytes != 4096 {
		t.Fatalf("cache inventory = %+v", caches)
	}
	testutil.FailErr(t, "remove seed by id", RemoveSeedByID(context.Background(), seeds, caches[0].ID))
	if _, err := os.Stat(enginepaths.ProjectSeedDir(seeds, primary)); !os.IsNotExist(err) {
		t.Fatalf("cleared seed cache remains: %v", err)
	}
}

func TestSeedCapacityEvictsLeastRecentlyUsedCache(t *testing.T) {
	mgr, _, seeds := newTestManager(t)
	oldKey := "0123456789abcdef"
	oldDir := filepath.Join(seeds, oldKey)
	testutil.FailErr(t, "mkdir old seed", os.MkdirAll(oldDir, 0o700))
	lastUsed := filepath.Join(oldDir, seedLastUsedFile)
	testutil.FailErr(t, "write old last-used", os.WriteFile(lastUsed, nil, 0o600))
	oldTime := time.Now().Add(-time.Hour)
	testutil.FailErr(t, "age old last-used", os.Chtimes(lastUsed, oldTime, oldTime))
	saved := queryAvailableStorageBytes
	t.Cleanup(func() { queryAvailableStorageBytes = saved })
	queryAvailableStorageBytes = func(string) (uint64, bool, error) {
		if _, err := os.Stat(oldDir); os.IsNotExist(err) {
			return 200, true, nil
		}
		return 10, true, nil
	}
	testutil.FailErr(t, "ensure seed capacity", mgr.ensureSeedCapacity(context.Background(), 100, "fedcba9876543210"))
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("least-recently-used seed was not evicted: %v", err)
	}
}

func assertFileBody(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read "+path, err)
	if string(raw) != want {
		t.Fatalf("%s = %q, want %q", path, raw, want)
	}
}
