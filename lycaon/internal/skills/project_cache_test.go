package skills

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectCacheReusesSnapshotWithinTTL(t *testing.T) {
	root := t.TempDir()
	writeProjectSkill(t, root, settingsoverlay.DirName()+"/skills", "cached", skillBody("cached", "d", "b\n"))

	var c ProjectCache
	got, _ := c.Discover([]string{root})
	if len(got) != 1 || got[0].Name != "cached" {
		t.Fatalf("got=%#v", got)
	}

	if err := os.RemoveAll(filepath.Join(root, settingsoverlay.DirName())); err != nil {
		testutil.FailErr(t, "remove", err)
	}
	again, _ := c.Discover([]string{root})
	if len(again) != 1 || again[0].Name != "cached" {
		t.Fatalf("snapshot must be reused within TTL: %#v", again)
	}
}

func TestProjectCacheExpires(t *testing.T) {
	root := t.TempDir()
	writeProjectSkill(t, root, settingsoverlay.DirName()+"/skills", "old", skillBody("old", "d", "b\n"))

	now := time.Now()
	c := ProjectCache{now: func() time.Time { return now }}
	got, _ := c.Discover([]string{root})
	if len(got) != 1 || got[0].Name != "old" {
		t.Fatalf("got=%#v", got)
	}

	writeProjectSkill(t, root, settingsoverlay.DirName()+"/skills", "fresh", skillBody("fresh", "d", "b\n"))
	now = now.Add(ProjectCacheTTL)
	again, _ := c.Discover([]string{root})
	if len(again) != 2 {
		t.Fatalf("expired snapshot must reread the repository: %#v", again)
	}
}

// Concurrent misses for the same root set share one walk, so an older walk
// cannot overwrite a newer result.
func TestProjectCacheCollapsesConcurrentMissesForSameKey(t *testing.T) {
	root := t.TempDir()
	writeProjectSkill(t, root, settingsoverlay.DirName()+"/skills", "concurrent", skillBody("concurrent", "d", "b\n"))

	var mu sync.Mutex
	calls := 0
	entered := make(chan struct{})
	release := make(chan struct{})
	c := ProjectCache{discover: func(roots []string) ([]Skill, []ProjectNote) {
		mu.Lock()
		calls++
		mu.Unlock()
		entered <- struct{}{}
		<-release
		return DiscoverProject(roots)
	}}

	const n = 5
	var wg sync.WaitGroup
	results := make([][]Skill, n)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, _ := c.Discover([]string{root})
			results[i] = got
		}(i)
	}

	<-entered
	// Give the other goroutines a chance to reach group.Do and queue behind
	// the in-flight call before releasing it.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("want exactly one walk for %d concurrent misses on the same key, got %d", n, got)
	}
	for i, r := range results {
		if len(r) != 1 || r[0].Name != "concurrent" {
			t.Fatalf("result[%d] = %#v", i, r)
		}
	}
}

func TestProjectCacheKeysByRootSet(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	writeProjectSkill(t, a, settingsoverlay.DirName()+"/skills", "from-a", skillBody("from-a", "d", "b\n"))
	writeProjectSkill(t, b, settingsoverlay.DirName()+"/skills", "from-b", skillBody("from-b", "d", "b\n"))

	var c ProjectCache
	gotA, _ := c.Discover([]string{a})
	gotB, _ := c.Discover([]string{b})
	if len(gotA) != 1 || gotA[0].Name != "from-a" {
		t.Fatalf("a=%#v", gotA)
	}
	if len(gotB) != 1 || gotB[0].Name != "from-b" {
		t.Fatalf("b=%#v", gotB)
	}
}
