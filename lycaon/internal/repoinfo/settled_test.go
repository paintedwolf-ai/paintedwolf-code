package repoinfo

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSettledHookFiresAfterAnalysis(t *testing.T) {
	dir := t.TempDir()
	p := newProviderWithAnalyze(func(context.Context, string) (*Brief, error) {
		return &Brief{FileCount: 1, GeneratedAt: time.Now()}, nil
	})
	t.Cleanup(func() { _ = p.Close() })
	var mu sync.Mutex
	var settled []string
	p.SetOnSettled(func(projectDir string) {
		mu.Lock()
		settled = append(settled, projectDir)
		mu.Unlock()
	})

	p.Warm(dir)
	testutil.WaitFor(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(settled) > 0
	})
	mu.Lock()
	defer mu.Unlock()
	if key, _ := cacheKey(dir); settled[0] != key {
		t.Fatalf("settled root = %q, want %q", settled[0], key)
	}
}
