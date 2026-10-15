package visual

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectStorageLocksReleaseIdleEntries(t *testing.T) {
	var registry projectStorageLockRegistry
	for i := 0; i < 64; i++ {
		registry.lock(strconv.Itoa(i))()
	}
	registry.mu.Lock()
	entries := len(registry.entries)
	registry.mu.Unlock()
	if entries != 0 {
		t.Fatalf("project storage locks = %d, want 0", entries)
	}
}

func TestArtifactRepairStatesStayBounded(t *testing.T) {
	var registry artifactRepairRegistry
	t.Cleanup(func() {
		registry.mu.Lock()
		defer registry.mu.Unlock()
		for key, entry := range registry.entries {
			entry.state.close()
			delete(registry.entries, key)
		}
	})
	root := t.TempDir()
	for i := 0; i < maxIdleArtifactRepairStates*2; i++ {
		state, release := registry.acquire(strconv.Itoa(i))
		dir, err := os.Open(root)
		testutil.FailErr(t, "open artifact directory", err)
		state.dir = dir
		release()
	}
	registry.mu.Lock()
	entries := len(registry.entries)
	registry.mu.Unlock()
	if entries > maxIdleArtifactRepairStates {
		t.Fatalf("artifact repair states = %d, want <= %d", entries, maxIdleArtifactRepairStates)
	}
}

func TestDurableStoreFirstAccessPrunesOrphanBlobs(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	wire, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "Put failed", err)
	orphan := []byte("orphaned artifact")
	orphanHash := artifactContentHash(orphan)
	_, _, err = putArtifactBlob(f.blobDir(t), orphanHash, orphan)
	testutil.FailErr(t, "write orphan blob", err)

	fresh := f.restart()
	if res := fresh.Resolve(t.Context(), "root-1", wire.ID); !res.IsPresent() {
		t.Fatalf("live artifact resolved as %q", res.Reason())
	}
	if _, ok := readArtifactBlob(f.blobDir(t), orphanHash, int64(len(orphan))); ok {
		t.Fatal("orphan blob survived reconciliation")
	}
}

func TestArtifactBlobPruningIsBounded(t *testing.T) {
	dir := t.TempDir()
	for i := range artifactPruneBatchSize + 1 {
		path := filepath.Join(dir, fmt.Sprintf("%064x", i+1))
		testutil.FailErr(t, "write orphan blob", os.WriteFile(path, []byte("orphan"), 0o600))
	}
	state := &artifactPruneState{}
	checked := 0
	isOrphan := func(string) (bool, error) {
		checked++
		return true, nil
	}
	done, err := pruneArtifactBlobBatch(dir, state, isOrphan)
	testutil.FailErr(t, "prune first batch", err)
	if done || checked != artifactPruneBatchSize {
		t.Fatalf("first batch: done=%v checked=%d", done, checked)
	}
	done, err = pruneArtifactBlobBatch(dir, state, isOrphan)
	testutil.FailErr(t, "prune second batch", err)
	if !done || checked != artifactPruneBatchSize+1 {
		t.Fatalf("second batch: done=%v checked=%d", done, checked)
	}
}

func TestDurableStoreListProjectAndTree(t *testing.T) {
	f := newDurableFixture(t, []string{"tree-a", "tree-b"})
	_, err := f.store.Put(t.Context(), "tree-a", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender, Caption: "a"},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "Put tree-a failed", err)
	_, err = f.store.Put(t.Context(), "tree-b", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture, Caption: "b", EvidenceHandle: "page#2"},
		Bytes: append(onePixelPNG(t), 0),
	})
	testutil.FailErr(t, "Put tree-b failed", err)

	page, err := f.store.ListProject(t.Context(), f.project, ArtifactPageQuery{})
	testutil.FailErr(t, "ListProject failed", err)
	all := page.Items
	if len(all) != 2 {
		t.Fatalf("ListProject = %d want 2", len(all))
	}
	for _, art := range all {
		if art.SessionID == "" || art.CreatedAt.IsZero() {
			t.Fatalf("list item missing session/created: %+v", art)
		}
	}
	tree, err := f.store.ListTree(t.Context(), "tree-a")
	testutil.FailErr(t, "ListTree failed", err)
	if len(tree) != 1 || tree[0].Caption != "a" || tree[0].SessionID != "tree-a" {
		t.Fatalf("ListTree = %+v", tree)
	}
}

func TestDurableStoreListsDraftArtifactsWithoutProject(t *testing.T) {
	store := NewDurableStore(DurableConfig{
		Lookup: func(context.Context, string) (string, error) { return "", nil },
	})
	wire, err := store.Put(t.Context(), "draft-root", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture, Caption: "draft"},
		Bytes: onePixelPNG(t),
	})
	testutil.FailErr(t, "put draft artifact", err)
	items, err := store.ListTree(t.Context(), "draft-root")
	testutil.FailErr(t, "list draft artifacts", err)
	if len(items) != 1 || items[0].ID != wire.ID || items[0].Caption != "draft" {
		t.Fatalf("draft artifacts = %+v", items)
	}
	if res := store.Resolve(t.Context(), "draft-root", wire.ID); !res.IsPresent() {
		t.Fatalf("draft artifact resolution = %+v", res)
	}
}

func TestDurableStoreDoesNotHideProjectLookupFailure(t *testing.T) {
	lookupErr := errors.New("project lookup failed")
	store := NewDurableStore(DurableConfig{
		Lookup: func(context.Context, string) (string, error) { return "", lookupErr },
	})
	if _, err := store.Put(t.Context(), "root", Entry{
		Meta: api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture}, Bytes: onePixelPNG(t),
	}); !errors.Is(err, lookupErr) {
		t.Fatalf("put error = %v", err)
	}
	if _, err := store.ListTree(t.Context(), "root"); !errors.Is(err, lookupErr) {
		t.Fatalf("list error = %v", err)
	}
	if res := store.Resolve(t.Context(), "root", "artifact"); res.Reason() != AbsenceUnavailable {
		t.Fatalf("resolve reason = %q", res.Reason())
	}
}

func TestDurableStoreListProjectUsesStableSeekPages(t *testing.T) {
	f := newDurableFixture(t, []string{"tree-a"})
	for _, caption := range []string{"a", "b", "c"} {
		_, err := f.store.Put(t.Context(), "tree-a", Entry{
			Meta: api.VisualArtifact{
				Mime: "image/png", Source: api.VisualArtifactSourceRender, Caption: caption,
			},
			Bytes: append(onePixelPNG(t), caption...),
		})
		testutil.FailErr(t, "Put "+caption, err)
	}
	first, err := f.store.ListProject(t.Context(), f.project, ArtifactPageQuery{Limit: 2})
	testutil.FailErr(t, "list first page", err)
	if len(first.Items) != 2 || first.NextCreatedAt == "" || first.NextID == "" {
		t.Fatalf("first page = %+v want two items and a cursor", first)
	}
	second, err := f.store.ListProject(t.Context(), f.project, ArtifactPageQuery{
		AfterCreatedAt: first.NextCreatedAt, AfterID: first.NextID, Limit: 2,
	})
	testutil.FailErr(t, "list second page", err)
	if len(second.Items) != 1 || second.NextCreatedAt != "" || second.NextID != "" {
		t.Fatalf("second page = %+v want one terminal item", second)
	}
	seen := map[string]struct{}{}
	for _, item := range append(first.Items, second.Items...) {
		if _, duplicate := seen[item.ID]; duplicate {
			t.Fatalf("artifact %s appeared in both pages", item.ID)
		}
		seen[item.ID] = struct{}{}
	}
	if len(seen) != 3 {
		t.Fatalf("paged artifacts = %d want 3", len(seen))
	}
}

func TestDurableStoreKeepsEveryRecordedArtifactBlob(t *testing.T) {
	f := newDurableFixture(t, []string{"root-1"})
	cited, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture, EvidenceHandle: "page#9"},
		Bytes: append(onePixelPNG(t), make([]byte, 800)...),
	})
	testutil.FailErr(t, "cited Put failed", err)
	claimed, err := f.store.Put(t.Context(), "root-1", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: append(onePixelPNG(t), append(make([]byte, 799), 1)...),
	})
	testutil.FailErr(t, "claimed Put failed", err)
	// The project cover is this artifact's only reference.
	testutil.FailErr(t, "cover claim failed", WriteRefsTx(t.Context(), f.queries, []RefWrite{{
		Kind: api.ArtifactReferenceKindProjectCover, ArtifactID: claimed.ID, ProjectID: f.project,
	}}))

	for i := 0; i < 8; i++ {
		body := append(onePixelPNG(t), make([]byte, 800)...)
		body[len(body)-1] = byte(i + 2)
		_, err := f.store.Put(t.Context(), "root-1", Entry{
			Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
			Bytes: body,
		})
		testutil.FailErr(t, "filler Put failed", err)
	}

	if !f.blobExists(t, f.record(t, cited.ID)) {
		t.Fatal("cited (evidence_handle) blob was pruned")
	}
	if !f.blobExists(t, f.record(t, claimed.ID)) {
		t.Fatal("claimed (project_cover reference) blob was pruned")
	}
	entries, _ := os.ReadDir(f.blobDir(t))
	blobs := 0
	for _, e := range entries {
		if !e.IsDir() {
			blobs++
		}
	}
	if blobs != 10 {
		t.Fatalf("recorded artifact blob count=%d want 10", blobs)
	}
}
