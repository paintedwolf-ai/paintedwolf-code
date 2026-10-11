package repoinfo

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPersistedOrientationIsStaleAndReusesClassifications(t *testing.T) {
	cache, root := t.TempDir(), t.TempDir()
	resolve := func(context.Context, string) (CatalogRoot, bool, error) {
		return CatalogRoot{ProjectID: "p", RootID: "r"}, true, nil
	}
	p := NewProvider(sourcecatalog.New().Trees, resolve, cache).(*fileProvider)
	stamp := time.Now().UTC()
	p.memo.replace(root, map[string]classifiedFile{"file": {size: 4, modified: stamp, lang: "Go"}})
	p.persistOrientation(root, &Brief{FileCount: 1, GeneratedAt: stamp})
	testutil.FailErr(t, "close first", p.Close())
	second := NewProvider(sourcecatalog.New().Trees, resolve, cache).(*fileProvider)
	defer func() { _ = second.Close() }()
	brief := second.cachedBrief(root)
	if brief == nil || brief.FileCount != 1 || Current(brief) {
		t.Fatalf("cached brief = %+v", brief)
	}
	second.loadClassifications(root)
	if lang, ok := second.memo.lookup(root, "file", 4, stamp); !ok || lang != "Go" {
		t.Fatalf("classification=%q, found=%t", lang, ok)
	}
	if _, ok := second.memo.lookup(root, "file", 5, stamp); ok {
		t.Fatal("reused changed file classification")
	}
}
