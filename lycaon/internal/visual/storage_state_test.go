package visual

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
	rawA := onePixelPNG(t)
	rawB := append(onePixelPNG(t), 0)
	_, err := f.store.Put(t.Context(), "tree-a", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender, Caption: "a"},
		Bytes: rawA,
	})
	testutil.FailErr(t, "Put tree-a failed", err)
	_, err = f.store.Put(t.Context(), "tree-b", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture, Caption: "b", EvidenceHandle: "page#2"},
		Bytes: rawB,
	})
	testutil.FailErr(t, "Put tree-b failed", err)

	page, err := f.store.ListProject(t.Context(), f.project, ArtifactPageQuery{})
	testutil.FailErr(t, "ListProject failed", err)
	all := page.Items
	if len(all) != 2 || all[0].Caption != "b" || all[1].Caption != "a" {
		t.Fatalf("ListProject = %+v want newest-first [b, a]", all)
	}
	if all[0].ContentHash != artifactContentHash(rawB) || all[1].ContentHash != artifactContentHash(rawA) {
		t.Fatalf("ListProject content hashes = [%q, %q]", all[0].ContentHash, all[1].ContentHash)
	}
	if len(page.SessionIDs) != 2 || page.SessionIDs[0] != "tree-b" || page.SessionIDs[1] != "tree-a" {
		t.Fatalf("ListProject session_ids = %+v want [tree-b, tree-a]", page.SessionIDs)
	}
	for _, art := range all {
		if art.SessionID == "" || art.CreatedAt.IsZero() {
			t.Fatalf("list item missing session/created: %+v", art)
		}
	}
	tree, err := f.store.ListTree(t.Context(), "tree-a")
	testutil.FailErr(t, "ListTree failed", err)
	if len(tree) != 1 || tree[0].Caption != "a" || tree[0].SessionID != "tree-a" || tree[0].ContentHash != artifactContentHash(rawA) {
		t.Fatalf("ListTree = %+v", tree)
	}
}

func TestDurableStoreListsDraftArtifactsWithoutProject(t *testing.T) {
	store := NewDurableStore(DurableConfig{
		Lookup: func(context.Context, string) (string, error) { return "", nil },
	})
	raw := onePixelPNG(t)
	wire, err := store.Put(t.Context(), "draft-root", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture, Caption: "draft"},
		Bytes: raw,
	})
	testutil.FailErr(t, "put draft artifact", err)
	items, err := store.ListTree(t.Context(), "draft-root")
	testutil.FailErr(t, "list draft artifacts", err)
	if len(items) != 1 || items[0].ID != wire.ID || items[0].Caption != "draft" || items[0].ContentHash != artifactContentHash(raw) {
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
	f := newDurableFixture(t, []string{"tree-a", "tree-b"})
	fixtures := []struct {
		tree    string
		source  api.VisualArtifactSource
		caption string
	}{
		{tree: "tree-a", source: api.VisualArtifactSourceRender, caption: "a"},
		{tree: "tree-b", source: api.VisualArtifactSourceCapture, caption: "b"},
		{tree: "tree-a", source: api.VisualArtifactSourceRender, caption: "c"},
		{tree: "tree-b", source: api.VisualArtifactSourceUser, caption: "d"},
	}
	var dID string
	for _, item := range fixtures {
		wire, err := f.store.Put(t.Context(), item.tree, Entry{
			Meta:  api.VisualArtifact{Mime: "image/png", Source: item.source, Caption: item.caption},
			Bytes: append(onePixelPNG(t), item.caption...),
		})
		testutil.FailErr(t, "Put "+item.caption, err)
		if item.caption == "d" {
			dID = wire.ID
		}
	}
	first, err := f.store.ListProject(t.Context(), f.project, ArtifactPageQuery{Limit: 2})
	testutil.FailErr(t, "list first page", err)
	if len(first.Items) != 2 || first.Items[0].Caption != "d" || first.Items[1].Caption != "c" || first.NextCreatedAt == "" || first.NextID == "" {
		t.Fatalf("first page = %+v want [d, c] and a cursor", first)
	}
	second, err := f.store.ListProject(t.Context(), f.project, ArtifactPageQuery{
		BeforeCreatedAt: first.NextCreatedAt, BeforeID: first.NextID, Limit: 2,
	})
	testutil.FailErr(t, "list second page", err)
	if len(second.Items) != 2 || second.Items[0].Caption != "b" || second.Items[1].Caption != "a" || second.NextCreatedAt != "" || second.NextID != "" {
		t.Fatalf("second page = %+v want terminal [b, a]", second)
	}
	if second.SessionIDs != nil {
		t.Fatalf("second page session_ids = %+v want nil on cursor page", second.SessionIDs)
	}
	seen := map[string]struct{}{}
	for _, item := range append(first.Items, second.Items...) {
		if _, duplicate := seen[item.ID]; duplicate {
			t.Fatalf("artifact %s appeared in both pages", item.ID)
		}
		seen[item.ID] = struct{}{}
	}
	if len(seen) != 4 {
		t.Fatalf("paged artifacts = %d want 4", len(seen))
	}

	renderFirst, err := f.store.ListProject(t.Context(), f.project, ArtifactPageQuery{
		Source: api.VisualArtifactSourceRender, Limit: 1,
	})
	testutil.FailErr(t, "list filtered render page 1", err)
	if len(renderFirst.Items) != 1 || renderFirst.Items[0].Caption != "c" || renderFirst.NextCreatedAt == "" {
		t.Fatalf("render page 1 = %+v want [c] with cursor", renderFirst)
	}
	if len(renderFirst.SessionIDs) != 2 || renderFirst.SessionIDs[0] != "tree-b" || renderFirst.SessionIDs[1] != "tree-a" {
		t.Fatalf("filtered session_ids = %+v want unfiltered [tree-b, tree-a]", renderFirst.SessionIDs)
	}
	renderSecond, err := f.store.ListProject(t.Context(), f.project, ArtifactPageQuery{
		Source:          api.VisualArtifactSourceRender,
		BeforeCreatedAt: renderFirst.NextCreatedAt,
		BeforeID:        renderFirst.NextID,
		Limit:           1,
	})
	testutil.FailErr(t, "list filtered render page 2", err)
	if len(renderSecond.Items) != 1 || renderSecond.Items[0].Caption != "a" || renderSecond.NextCreatedAt != "" || renderSecond.SessionIDs != nil {
		t.Fatalf("render page 2 = %+v want terminal [a] without session_ids", renderSecond)
	}

	bySession, err := f.store.ListProject(t.Context(), f.project, ArtifactPageQuery{SessionID: "tree-b"})
	testutil.FailErr(t, "list session tree-b", err)
	if len(bySession.Items) != 2 || bySession.Items[0].Caption != "d" || bySession.Items[1].Caption != "b" {
		t.Fatalf("session tree-b = %+v want [d, b]", bySession.Items)
	}

	_, _, err = f.store.Delete(t.Context(), f.project, dID, "human_delete")
	testutil.FailErr(t, "delete d", err)
	afterDelete, err := f.store.ListProject(t.Context(), f.project, ArtifactPageQuery{Source: api.VisualArtifactSourceCapture})
	testutil.FailErr(t, "list after delete", err)
	if len(afterDelete.SessionIDs) != 2 || afterDelete.SessionIDs[0] != "tree-a" || afterDelete.SessionIDs[1] != "tree-b" {
		t.Fatalf("session_ids after deleting newest tree-b artifact = %+v want [tree-a, tree-b]", afterDelete.SessionIDs)
	}
}

func TestPageProjectArtifactsUsesCreatedAtIndexSeek(t *testing.T) {
	f := newDurableFixture(t, []string{"tree-a"})
	rawSQL, err := os.ReadFile(filepath.Join("..", "db", "queries", "artifacts.sql"))
	testutil.FailErr(t, "read artifacts.sql", err)
	const header = "-- name: PageProjectArtifacts :many\n"
	_, rest, ok := strings.Cut(string(rawSQL), header)
	if !ok {
		t.Fatal("PageProjectArtifacts block not found in artifacts.sql")
	}
	stmt, _, ok := strings.Cut(rest, ";")
	if !ok {
		t.Fatal("PageProjectArtifacts statement terminator missing")
	}
	query := regexp.MustCompile(`sqlc\.arg\([a-z_]+\)`).ReplaceAllString(stmt, "?")
	args := make([]any, strings.Count(query, "?"))
	for i := range args {
		args[i] = ""
	}
	args[len(args)-1] = 13
	rows, err := f.sqlDB.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query, args...)
	testutil.FailErr(t, "explain PageProjectArtifacts", err)
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		testutil.FailErr(t, "scan explain row", rows.Scan(&id, &parent, &notused, &detail))
		details = append(details, detail)
	}
	testutil.FailErr(t, "iterate explain rows", rows.Err())
	plan := strings.Join(details, "\n")
	if !strings.Contains(plan, "idx_artifacts_project_created") || !strings.Contains(plan, "created_at<?") {
		t.Fatalf("PageProjectArtifacts plan lost created_at index range seek:\n%s", plan)
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
