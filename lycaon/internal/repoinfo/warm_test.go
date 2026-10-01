package repoinfo_test

import (
	"context"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBriefReturnsPlaceholderUntilWarmCompletes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	p := repotest.NewProvider(t)
	brief, err := p.Brief(context.Background(), dir)
	testutil.FailErr(t, "Brief", err)
	// The first miss returns an unmeasured placeholder at once, with no count.
	if brief.Materialized || brief.FileCount != 0 {
		t.Fatalf("first miss = %+v, want an unmeasured placeholder", brief)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	materialized, err := repoinfo.AwaitBrief(ctx, p, dir)
	testutil.FailErr(t, "AwaitBrief", err)
	if !materialized.Materialized {
		t.Fatal("AwaitBrief returned unmaterialized brief")
	}
	if materialized.FileCount != 1 {
		t.Fatalf("FileCount = %d, want 1", materialized.FileCount)
	}
}

func TestCloseDrainsAndDisablesWarm(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	p := repotest.NewProvider(t)

	if err := p.Close(); err != nil {
		testutil.FailErr(t, "Close", err)
	}
	if err := p.Close(); err != nil {
		testutil.FailErr(t, "Close (idempotent)", err)
	}

	// Closed providers remain inert.
	p.Warm(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if brief, err := repoinfo.AwaitBrief(ctx, p, dir); err == nil && brief.Materialized {
		t.Fatal("brief materialized after Close, want warm disabled")
	}
}

func TestKnownEmptyMeasuresTheIndexNotTheBrief(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	p := repotest.NewProvider(t)
	defer func() { _ = p.Close() }()

	empty := t.TempDir()
	_, err := repoinfo.AwaitBrief(testutil.BoundedContext(t, 5*time.Second), p, empty)
	testutil.FailErr(t, "warm empty root", err)
	got, err := p.KnownEmpty(t.Context(), empty)
	testutil.FailErr(t, "measure empty root", err)
	if !got {
		t.Fatal("a measured empty tree did not report empty")
	}

	occupied := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(occupied, "main.go"), []byte("package main\n"), 0o644))
	got, err = p.KnownEmpty(t.Context(), occupied)
	testutil.FailErr(t, "measure occupied root", err)
	if got {
		t.Fatal("a tree holding a file reported empty")
	}
}

func TestProgressiveBriefCacheHitAfterBackgroundFinish(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	p := repotest.NewProvider(t)
	defer func() { _ = p.Close() }()

	first, err := p.Brief(context.Background(), dir)
	testutil.FailErr(t, "Brief", err)
	if first.Materialized {
		t.Fatal("first Brief should be an unmeasured placeholder")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = repoinfo.AwaitBrief(ctx, p, dir)
	testutil.FailErr(t, "AwaitBrief", err)

	second, err := p.Brief(context.Background(), dir)
	testutil.FailErr(t, "Brief after", err)
	if !second.Materialized {
		t.Fatal("want cache_hit materialized brief")
	}
	if second.FileCount != 1 {
		t.Fatalf("FileCount = %d want 1", second.FileCount)
	}
}
