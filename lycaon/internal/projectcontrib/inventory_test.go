package projectcontrib

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/governance"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestInventoryReusesDiscoveryAndReadsCurrentContent(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "canonical root", err)
	i := NewInventory()
	t.Cleanup(i.Close)
	var walks atomic.Int32
	i.walk = func(ctx context.Context, root string) ([]governance.ResolvedAgentsMD, error) {
		walks.Add(1)
		return governance.ListIndex(ctx, root)
	}
	write := func(rel, text string) {
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755))
		testutil.FailErr(t, "write", os.WriteFile(filepath.Join(root, rel), []byte(text), 0o600))
	}
	scan := func() Surface {
		m, err := i.Scan(t.Context(), []string{root})
		testutil.FailErr(t, "scan", err)
		found, _ := m.Surface(SurfaceAgentsMD)
		return found
	}
	write("AGENTS.md", "first")
	first := scan()
	for range 10 {
		scan()
	}
	if walks.Load() != 1 {
		t.Fatalf("warm reads walked %d times", walks.Load())
	}
	write("AGENTS.md", "changed")
	if scan().Files[0].Content == first.Files[0].Content {
		t.Fatal("captured content was stale")
	}
	write("ordinary.txt", "edit")
	i.changed(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{"ordinary.txt"}})
	scan()
	if walks.Load() != 1 {
		t.Fatal("ordinary file edit walked the repository")
	}
	write("new/sub/AGENTS.md", "nested")
	i.changed(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{"new"}})
	if scan().Count != 2 {
		t.Fatal("new subtree was not discovered")
	}
	if walks.Load() != 2 {
		t.Fatalf("subtree discovery walks = %d", walks.Load())
	}
	testutil.FailErr(t, "remove nested guidance", os.Remove(filepath.Join(root, "new/sub/AGENTS.md")))
	i.changed(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{"new/sub/AGENTS.md"}})
	if scan().Count != 1 {
		t.Fatal("removed instruction remains in inventory")
	}
}

func TestInventoryServesRecentIndexToStatusReadsWhileDirty(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "canonical root", err)
	i := NewInventory()
	t.Cleanup(i.Close)
	var walks atomic.Int32
	i.walk = func(ctx context.Context, root string) ([]governance.ResolvedAgentsMD, error) {
		walks.Add(1)
		return governance.ListIndex(ctx, root)
	}
	testutil.FailErr(t, "write guidance", os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("first"), 0o600))
	count := func(m Manifest) int { found, _ := m.Surface(SurfaceAgentsMD); return found.Count }
	recent, err := i.ScanRecent(t.Context(), []string{root}, time.Hour)
	testutil.FailErr(t, "cold status read", err)
	if count(recent) != 1 || walks.Load() != 1 {
		t.Fatalf("cold status read: count=%d walks=%d", count(recent), walks.Load())
	}
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(root, "new/sub"), 0o755))
	testutil.FailErr(t, "write nested", os.WriteFile(filepath.Join(root, "new/sub/AGENTS.md"), []byte("nested"), 0o600))
	i.changed(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{"new"}})
	// A status read inside the window answers from the recent index without a walk.
	recent, err = i.ScanRecent(t.Context(), []string{root}, time.Hour)
	testutil.FailErr(t, "recent status read", err)
	if count(recent) != 1 || walks.Load() != 1 {
		t.Fatalf("recent status read: count=%d walks=%d", count(recent), walks.Load())
	}
	// Acting on the inventory always reads current discovery.
	fresh, err := i.Scan(t.Context(), []string{root})
	testutil.FailErr(t, "fresh scan", err)
	if count(fresh) != 2 || walks.Load() != 2 {
		t.Fatalf("fresh scan: count=%d walks=%d", count(fresh), walks.Load())
	}
	i.changed(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{"new"}})
	i.mu.Lock()
	for _, entry := range i.roots {
		entry.validated = time.Now().Add(-time.Hour)
	}
	i.mu.Unlock()
	// Past the window a status read walks like any other.
	if _, err := i.ScanRecent(t.Context(), []string{root}, time.Minute); err != nil {
		t.Fatalf("aged status read: %v", err)
	}
	if walks.Load() != 3 {
		t.Fatalf("aged status read walks = %d", walks.Load())
	}
}

func TestInventorySharesCanonicalRootsAndReceivesWatcherPaths(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "canonical root", err)
	alias := filepath.Join(t.TempDir(), "linked-root")
	testutil.FailErr(t, "link root", os.Symlink(root, alias))
	i := NewInventory()
	t.Cleanup(i.Close)
	_, err = i.Scan(t.Context(), []string{alias})
	testutil.FailErr(t, "scan linked root", err)
	testutil.FailErr(t, "write guidance", os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("new guidance"), 0o600))
	i.changed(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{"AGENTS.md"}})
	manifest, err := i.Scan(t.Context(), []string{alias})
	testutil.FailErr(t, "scan updated linked root", err)
	surface, _ := manifest.Surface(SurfaceAgentsMD)
	i.mu.Lock()
	rootCount := len(i.roots)
	i.mu.Unlock()
	if surface.Count != 1 || rootCount != 1 {
		t.Fatalf("linked root inventory: count=%d roots=%d", surface.Count, rootCount)
	}
}

func TestInventorySharesColdReadAndAllowsWaiterCancellation(t *testing.T) {
	i := NewInventory()
	t.Cleanup(i.Close)
	root := t.TempDir()
	started, release := make(chan struct{}), make(chan struct{})
	var walks atomic.Int32
	i.walk = func(context.Context, string) ([]governance.ResolvedAgentsMD, error) {
		if walks.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { _, err := i.Scan(ctx, []string{root}); first <- err }()
	<-started
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			_, err := i.Scan(t.Context(), []string{root})
			if err != nil {
				t.Errorf("scan: %v", err)
			}
		})
	}
	close(release)
	group.Wait()
	if walks.Load() != 1 {
		t.Fatalf("cold discovery repeated %d times", walks.Load())
	}
}

func TestTrustReadRetainsEmptyExtensionFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".paintedwolf")
	testutil.FailErr(t, "create overlay", os.MkdirAll(dir, 0o700))
	testutil.FailErr(t, "write empty extensions", os.WriteFile(filepath.Join(dir, "extensions.yaml"), nil, 0o600))
	i := NewInventory()
	t.Cleanup(i.Close)
	manifest, err := i.Scan(t.Context(), []string{root})
	testutil.FailErr(t, "scan empty extensions", err)
	for _, id := range []string{SurfaceExtensionConfig, SurfaceExtensionSuggestions} {
		surface, _ := manifest.Surface(id)
		if len(surface.Files) != 1 || surface.Files[0].Content != "" {
			t.Fatalf("empty file missing from %s: %+v", id, surface.Files)
		}
	}
}

func TestTrustReadRefusesIncompleteDirectoryDiscovery(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".paintedwolf")
	testutil.FailErr(t, "create overlay", os.MkdirAll(dir, 0o700))
	testutil.FailErr(t, "replace rules directory with file", os.WriteFile(filepath.Join(dir, "rules"), []byte("not a directory"), 0o600))
	i := NewInventory()
	t.Cleanup(i.Close)
	if _, err := i.Scan(t.Context(), []string{root}); err == nil {
		t.Fatal("invalid configuration directory produced a complete snapshot")
	}
}

func TestTrustReadRefusesEscapingLinkAndOversizedConfiguration(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private.txt")
	testutil.FailErr(t, "write outside file", os.WriteFile(outside, []byte("outside"), 0o600))
	overlay := filepath.Join(root, ".paintedwolf")
	testutil.FailErr(t, "create overlay", os.MkdirAll(overlay, 0o755))
	target := filepath.Join(overlay, "approvals.yaml")
	testutil.FailErr(t, "link outside configuration", os.Symlink(outside, target))
	inventory := NewInventory()
	t.Cleanup(inventory.Close)
	if _, err := inventory.Scan(t.Context(), []string{root}); err == nil {
		t.Fatal("escaping link was accepted as reviewable configuration")
	}
	testutil.FailErr(t, "remove link", os.Remove(target))
	testutil.FailErr(t, "write oversized configuration", os.WriteFile(target, make([]byte, maxTrustFileBytes+1), 0o600))
	if _, err := inventory.Scan(t.Context(), []string{root}); err == nil {
		t.Fatal("oversized configuration was silently omitted")
	}
}

func TestInventoryDiscoveryUsesOwnerLifetime(t *testing.T) {
	i := NewInventory()
	t.Cleanup(i.Close)
	started := make(chan bool, 1)
	i.walk = func(ctx context.Context, _ string) ([]governance.ResolvedAgentsMD, error) {
		_, deadline := ctx.Deadline()
		started <- deadline
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan error, 1)
	root := t.TempDir()
	go func() { _, err := i.Scan(t.Context(), []string{root}); done <- err }()
	if <-started {
		t.Error("discovery has a fixed lifetime that excludes slower repositories")
	}
	i.Close()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("close discovery: %v", err)
	}
}
