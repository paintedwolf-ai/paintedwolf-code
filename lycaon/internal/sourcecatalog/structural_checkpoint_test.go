package sourcecatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func checkpointTestStore(t *testing.T) *indexStore {
	t.Helper()
	return &indexStore{catalog: New(), structureFile: filepath.Join(t.TempDir(), "snapshot.tree"),
		storeCore: storeCore{root: Root{ID: "checkpoint-root", Path: t.TempDir()}}}
}

func checkpointTestGeneration(t *testing.T, store *indexStore, base *structuralGeneration, count int) *structuralGeneration {
	t.Helper()
	return checkpointTestGenerationWithMemory(t, store, base, count, structuralMemoryLimit)
}

func checkpointTestGenerationWithMemory(t *testing.T, store *indexStore, base *structuralGeneration, count int, memoryLimit int64) *structuralGeneration {
	t.Helper()
	builder, err := newStructuralBuilder(store, base)
	testutil.FailErr(t, "create structural builder", err)
	defer builder.close()
	if memoryLimit != structuralMemoryLimit {
		testutil.FailErr(t, "close default checkpoint writer", builder.writer.Close())
		writer, err := newStructuralSegmentWriter(filepath.Dir(store.structureFile), memoryLimit)
		testutil.FailErr(t, "create checkpoint fixture writer", err)
		builder.writer = writer
	}
	index, _, err := builder.children(t.Context(), ".")
	testutil.FailErr(t, "open checkpoint directory", err)
	items := make([]pagedview.RangeItem[TreeItem], count)
	for i := range items {
		name := fmt.Sprintf("file-%04d", i)
		items[i], err = builder.item(t.Context(), indexNode{path: name, name: name, regular: true})
		testutil.FailErr(t, "construct checkpoint item", err)
		items[i].Value.Sequence = 7
	}
	testutil.FailErr(t, "populate structural range", index.SetBatch(t.Context(), items))
	testutil.FailErr(t, "save structural range", builder.save(t.Context(), index, DirectoryObservation{Path: ".", Sequence: 7, FirstListed: 2, Entries: count, Complete: true, Observed: time.Unix(20, 0)}))
	generation, err := builder.seal(t.Context(), 7)
	testutil.FailErr(t, "seal structural generation", err)
	t.Cleanup(generation.close)
	return generation
}

func TestStructuralCheckpointRoundTripAndCompaction(t *testing.T) {
	store := checkpointTestStore(t)
	first := checkpointTestGeneration(t, store, nil, 300)
	next := checkpointTestGenerationWithMemory(t, store, first, 400, 16<<10)
	sealedSpill := false
	for _, segment := range next.segments {
		if segment.bytes.spill != nil && segment.bytes.sealed {
			sealedSpill = true
			break
		}
	}
	if !sealedSpill {
		t.Fatal("checkpoint fixture did not seal spilled segment for reopen")
	}
	var output bytes.Buffer
	testutil.FailErr(t, "write checkpoint", writeStructuralCheckpoint(t.Context(), &output, store.root, next))
	loaded, err := readStructuralCheckpoint(t.Context(), bytes.NewReader(output.Bytes()), store)
	testutil.FailErr(t, "load checkpoint", err)
	defer loaded.close()
	work, err := newStructuralCompaction(t.Context(), store, next)
	testutil.FailErr(t, "start compaction", err)
	defer work.close()
	compacted, err := work.seal(t.Context())
	testutil.FailErr(t, "compact generation", err)
	defer compacted.close()
	for _, generation := range []*structuralGeneration{loaded, compacted} {
		if len(generation.segments) != 1 || generation.id != next.id {
			t.Fatalf("compacted generation: segments %d, id %d", len(generation.segments), generation.id)
		}
		root, ok, err := generation.directories.Get(t.Context(), ".")
		testutil.FailErr(t, "read checkpoint root", err)
		if !ok || root.observation.Entries != 400 || !root.observation.Complete {
			t.Fatalf("loaded observation = %+v", root.observation)
		}
		index := pagedview.RangeIndex[TreeItem]{Store: generation, Root: root.page}
		extent, err := index.Extent(t.Context())
		testutil.FailErr(t, "read loaded extent", err)
		if extent != 400 {
			t.Fatalf("loaded extent = %d", extent)
		}
		for _, rank := range []int64{0, 127, 128, 399} {
			item, err := index.SelectItem(t.Context(), rank)
			testutil.FailErr(t, "select loaded item", err)
			if item.Value.Path != fmt.Sprintf("file-%04d", rank) {
				t.Fatalf("rank %d = %q", rank, item.Value.Path)
			}
		}
	}
	root, _, err := first.directories.Get(t.Context(), ".")
	testutil.FailErr(t, "read retained root", err)
	index := pagedview.RangeIndex[TreeItem]{Store: first, Root: root.page}
	extent, err := index.Extent(t.Context())
	testutil.FailErr(t, "read retained generation", err)
	if extent != 300 {
		t.Fatalf("retained generation changed to %d", extent)
	}
}

func TestStructuralCheckpointFrameBounds(t *testing.T) {
	for _, test := range []struct {
		kind   byte
		length uint32
	}{
		{'H', structuralCheckpointMetadataLimit + 1},
		{'P', maxStructuralSegmentEntry + 1},
	} {
		var header [5]byte
		header[0] = test.kind
		binary.LittleEndian.PutUint32(header[1:], test.length)
		if _, _, err := readStructuralFrame(t.Context(), bytes.NewReader(header[:])); !errors.Is(err, pagedview.ErrBudget) {
			t.Fatalf("oversized %q frame error = %v, want budget", test.kind, err)
		}
	}
}

func TestStructuralCheckpointRejectsCorruptionAndWrongIdentity(t *testing.T) {
	store := checkpointTestStore(t)
	generation := checkpointTestGeneration(t, store, nil, 2)
	var output bytes.Buffer
	testutil.FailErr(t, "write valid checkpoint", writeStructuralCheckpoint(t.Context(), &output, store.root, generation))
	corrupt := bytes.Clone(output.Bytes())
	corrupt[len(corrupt)-1] ^= 1
	for _, input := range [][]byte{corrupt, output.Bytes()[:len(output.Bytes())-1], append(bytes.Clone(output.Bytes()), 0)} {
		if loaded, err := readStructuralCheckpoint(t.Context(), bytes.NewReader(input), store); err == nil {
			loaded.close()
			t.Fatal("invalid checkpoint accepted")
		}
	}
	other := checkpointTestStore(t)
	other.root = Root{ID: store.root.ID, Path: "/other"}
	if loaded, err := readStructuralCheckpoint(t.Context(), bytes.NewReader(output.Bytes()), other); err == nil {
		loaded.close()
		t.Fatal("checkpoint for another root path accepted")
	}
	// The path identifies the root: a project that reattaches it restores the same checkpoint.
	reattached := checkpointTestStore(t)
	reattached.root = Root{ID: "other", Path: store.root.Path}
	loaded, err := readStructuralCheckpoint(t.Context(), bytes.NewReader(output.Bytes()), reattached)
	testutil.FailErr(t, "load checkpoint under another root id", err)
	loaded.close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readStructuralCheckpoint(ctx, bytes.NewReader(output.Bytes()), store); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled load = %v", err)
	}
}

func TestStructuralCheckpointRejectsInvalidPageGraphs(t *testing.T) {
	store := checkpointTestStore(t)
	fingerprint := TreeRowFingerprint("a", "file", false, false, "")
	leaf := pagedview.RangePage[TreeItem]{Items: []pagedview.RangeItem[TreeItem]{{Key: DirectoryOrder("a", false), Value: TreeItem{Path: "a"}, Weight: 1, Fingerprint: fingerprint, BaselineFingerprint: fingerprint}}}
	for _, test := range []struct {
		name  string
		pages []pagedview.RangePage[TreeItem]
		root  uint64
	}{
		{"missing root", nil, 1},
		{"unreachable page", []pagedview.RangePage[TreeItem]{leaf}, 0},
		{"forward cycle", []pagedview.RangePage[TreeItem]{{Children: []pagedview.Branch{{Key: DirectoryOrder("a", false), Page: 1, Count: 1, Weight: 1}}}}, 1},
		{"incorrect aggregate", []pagedview.RangePage[TreeItem]{leaf, {Children: []pagedview.Branch{{Key: DirectoryOrder("a", false), Page: 1, Count: 1, Weight: 2}}}}, 2},
		{"duplicate key", []pagedview.RangePage[TreeItem]{{Items: append(leaf.Items, leaf.Items...)}}, 1},
		{"wrong parent", []pagedview.RangePage[TreeItem]{{Items: []pagedview.RangeItem[TreeItem]{{Key: DirectoryOrder("a", false), Value: TreeItem{Path: "other/a"}, Weight: 1}}}}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded := checkpointGraphFixture(t, store, test.pages, test.root)
			if generation, err := readStructuralCheckpoint(t.Context(), bytes.NewReader(encoded), store); err == nil {
				generation.close()
				t.Fatal("invalid graph accepted despite valid checksum")
			}
		})
	}
}

func checkpointGraphFixture(t *testing.T, store *indexStore, pages []pagedview.RangePage[TreeItem], root uint64) []byte {
	t.Helper()
	var output bytes.Buffer
	testutil.FailErr(t, "write fixture header", writeStructuralJSON(&output, 'H', structuralCheckpointHeader{Format: structuralFormat, RootPath: []byte(store.root.Path), Generation: 7, Directories: 1}))
	for _, page := range pages {
		body, err := encodeRangePage(page)
		testutil.FailErr(t, "encode fixture page", err)
		testutil.FailErr(t, "write fixture page", writeStructuralFrame(&output, 'P', body))
	}
	testutil.FailErr(t, "write fixture directory", writeStructuralJSON(&output, 'D', structuralCheckpointDirectory{Page: root, Path: []byte("."), Observation: DirectoryObservation{Sequence: 7, FirstListed: 1, Complete: true}}))
	testutil.FailErr(t, "write fixture end", writeStructuralFrame(&output, 0, nil))
	digest := sha256.Sum256(output.Bytes())
	return append(output.Bytes(), digest[:]...)
}

func TestStructuralCheckpointRejectsForgedRowMeasures(t *testing.T) {
	store := checkpointTestStore(t)
	fingerprint := TreeRowFingerprint("a", "file", false, false, "")
	for _, test := range []struct {
		name  string
		alter func(*pagedview.RangeItem[TreeItem])
	}{
		{"file weight", func(item *pagedview.RangeItem[TreeItem]) { item.Weight = 2 }},
		{"file unresolved", func(item *pagedview.RangeItem[TreeItem]) { item.Unresolved = 1 }},
		{"row fingerprint", func(item *pagedview.RangeItem[TreeItem]) { item.Fingerprint = pagedview.Fingerprint{} }},
		{"baseline fingerprint", func(item *pagedview.RangeItem[TreeItem]) { item.BaselineFingerprint = pagedview.Fingerprint{} }},
		{"future row sequence", func(item *pagedview.RangeItem[TreeItem]) { item.Value.Sequence = 8 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := pagedview.RangeItem[TreeItem]{Key: DirectoryOrder("a", false), Value: TreeItem{Path: "a"}, Weight: 1, Fingerprint: fingerprint, BaselineFingerprint: fingerprint}
			test.alter(&item)
			body := checkpointGraphFixture(t, store, []pagedview.RangePage[TreeItem]{{Items: []pagedview.RangeItem[TreeItem]{item}}}, 1)
			if loaded, err := readStructuralCheckpoint(t.Context(), bytes.NewReader(body), store); err == nil {
				loaded.close()
				t.Fatal("forged row measures accepted with valid structural checksum")
			}
		})
	}
}

func TestStructuralCheckpointRejectsCompleteUnlistedDirectory(t *testing.T) {
	store := checkpointTestStore(t)
	builder, err := newStructuralBuilder(store, nil)
	testutil.FailErr(t, "create malformed directory fixture", err)
	defer builder.close()
	testutil.FailErr(t, "save malformed complete observation", builder.save(t.Context(), &pagedview.RangeIndex[TreeItem]{Store: builder}, DirectoryObservation{Path: ".", Complete: true}))
	generation, err := builder.seal(t.Context(), 1)
	testutil.FailErr(t, "seal malformed directory fixture", err)
	defer generation.close()
	var output bytes.Buffer
	testutil.FailErr(t, "encode malformed directory fixture", writeStructuralCheckpoint(t.Context(), &output, store.root, generation))
	if loaded, err := readStructuralCheckpoint(t.Context(), bytes.NewReader(output.Bytes()), store); err == nil {
		loaded.close()
		t.Fatal("complete directory without observation identity was accepted")
	}
}

func TestStructuralCheckpointAtomicWriteAndDrain(t *testing.T) {
	store := checkpointTestStore(t)
	store.structure = checkpointTestGeneration(t, store, nil, 20)
	store.scheduleStructuralCheckpoint(t.Context())
	store.mu.Lock()
	done := store.checkpoint.done
	store.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-t.Context().Done():
			t.Fatal("checkpoint did not settle")
		}
	}
	store.mu.Lock()
	err := store.checkpoint.lastError
	store.mu.Unlock()
	testutil.FailErr(t, "asynchronous checkpoint", err)
	loaded, err := loadStructuralCheckpoint(t.Context(), store, false)
	testutil.FailErr(t, "load atomic checkpoint", err)
	loaded.close()
	store.mu.Lock()
	store.drainStructuralCheckpointLocked()
	store.mu.Unlock()
	store.scheduleStructuralCheckpoint(t.Context())
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.checkpoint.done != nil {
		t.Fatal("checkpoint admitted after drain")
	}
}

func TestStructuralCheckpointDrainCancelsQueuedIO(t *testing.T) {
	store := checkpointTestStore(t)
	store.structure = checkpointTestGeneration(t, store, nil, 2)
	store.catalog.broker = backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{backgroundwork.ResourceIO: {Total: 1}})
	release, err := store.catalog.broker.Acquire(t.Context(), backgroundwork.Request{Resources: []backgroundwork.Resource{backgroundwork.ResourceIO}})
	testutil.FailErr(t, "hold checkpoint IO capacity", err)
	defer release()
	for range 100 {
		store.scheduleStructuralCheckpoint(t.Context())
	}
	store.mu.Lock()
	done := store.drainStructuralCheckpointLocked()
	store.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-t.Context().Done():
			t.Fatal("queued checkpoint did not cancel")
		}
	}
	if _, err := os.Stat(store.structureFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled checkpoint created cache: %v", err)
	}
}

func TestStructuralCheckpointRetention(t *testing.T) {
	catalog := New()
	catalog.treeDir = t.TempDir()
	file := filepath.Join(catalog.treeDir, "expired.tree")
	testutil.FailErr(t, "write expired cache", os.WriteFile(file, []byte("cache"), 0o600))
	old := time.Now().Add(-2 * defaultTreeStorePolicy().retention)
	testutil.FailErr(t, "age expired cache", os.Chtimes(file, old, old))
	// Visible scratch files are orphaned only after the grace period.
	orphan := filepath.Join(catalog.treeDir, "structural-segments-1.tmp")
	testutil.FailErr(t, "write orphaned scratch", os.WriteFile(orphan, []byte("spill"), 0o600))
	testutil.FailErr(t, "age orphaned scratch", os.Chtimes(orphan, old, old))
	fresh := filepath.Join(catalog.treeDir, "structural-scan-2.tmp")
	testutil.FailErr(t, "write fresh scratch", os.WriteFile(fresh, []byte("spool"), 0o600))
	removed, err := catalog.ReconcileTreeStores(t.Context())
	testutil.FailErr(t, "reconcile structural caches", err)
	if removed != 1 {
		t.Fatalf("removed structural caches = %d", removed)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired structural cache remains: %v", err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphaned structural scratch remains: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh structural scratch removed: %v", err)
	}
}

func TestStructuralCheckpointCompactionPreservesPinnedGeneration(t *testing.T) {
	store := checkpointTestStore(t)
	for generation := int64(1); generation <= 17; generation++ {
		builder, err := newStructuralBuilder(store, store.structure)
		testutil.FailErr(t, "create incremental builder", err)
		index, _, err := builder.children(t.Context(), ".")
		testutil.FailErr(t, "open incremental directory", err)
		item, err := builder.item(t.Context(), indexNode{path: "a", name: "a", regular: true})
		testutil.FailErr(t, "construct incremental directory item", err)
		testutil.FailErr(t, "populate incremental directory", index.SetBatch(t.Context(), []pagedview.RangeItem[TreeItem]{item}))
		testutil.FailErr(t, "save incremental directory", builder.save(t.Context(), index, DirectoryObservation{Path: ".", Sequence: generation, FirstListed: 1, Entries: 1, Complete: true}))
		next, err := builder.seal(t.Context(), generation)
		builder.close()
		testutil.FailErr(t, "seal incremental directory", err)
		store.mu.Lock()
		store.initializeStructureLocked()
		store.installStructureLocked(next)
		store.mu.Unlock()
	}
	defer func() {
		store.mu.Lock()
		store.releaseCompletedStructureLocked()
		if store.structure != nil {
			store.structure.close()
			store.structure = nil
		}
		store.mu.Unlock()
	}()
	// Copy-on-write debt triggers compaction independently of segment count.
	store.structure.compactionDebtBytes = structuralCompactionDebtBytes
	store.structure.encodedBytes = 2 * structuralCompactionDebtBytes
	retained, err := store.retainGeneration(0, true)
	testutil.FailErr(t, "pin original generation", err)
	defer retained.Release()
	written, err := store.checkpointStructure(t.Context())
	testutil.FailErr(t, "checkpoint and compact structure", err)
	if !written {
		t.Fatal("complete head was not checkpointed")
	}
	if store.structure.id != 18 || len(store.structure.segments) != 1 {
		t.Fatalf("compacted head: generation %d, segments %d", store.structure.id, len(store.structure.segments))
	}
	root, _, err := retained.value.directories.Get(t.Context(), ".")
	testutil.FailErr(t, "read pinned predecessor root", err)
	index := pagedview.RangeIndex[TreeItem]{Store: retained.value, Root: root.page}
	item, err := index.SelectItem(t.Context(), 0)
	testutil.FailErr(t, "read pinned predecessor", err)
	if item.Value.Path != "a" {
		t.Fatalf("pinned predecessor item = %+v", item)
	}
}

func TestStructuralCheckpointPreservesRawPathsAndSequenceClock(t *testing.T) {
	store := checkpointTestStore(t)
	builder, err := newStructuralBuilder(store, nil)
	testutil.FailErr(t, "create raw path builder", err)
	defer builder.close()
	wantSequence := structuralObservationSerial.Load() + 1000
	observation := DirectoryObservation{Path: "name-\xff", Sequence: wantSequence, FirstListed: wantSequence, Complete: true}
	testutil.FailErr(t, "save raw path directory", builder.save(t.Context(), &pagedview.RangeIndex[TreeItem]{Store: builder}, observation))
	generation, err := builder.seal(t.Context(), 1)
	testutil.FailErr(t, "seal raw path directory", err)
	defer generation.close()
	var output bytes.Buffer
	testutil.FailErr(t, "encode raw path checkpoint", writeStructuralCheckpoint(t.Context(), &output, store.root, generation))
	loaded, err := readStructuralCheckpoint(t.Context(), bytes.NewReader(output.Bytes()), store)
	testutil.FailErr(t, "decode raw path checkpoint", err)
	defer loaded.close()
	if directory, ok, err := loaded.directories.Get(t.Context(), observation.Path); err != nil || !ok || directory.observation.Sequence != wantSequence {
		t.Fatal("checkpoint changed raw directory bytes or observation sequence")
	}
	if structuralObservationSerial.Load() < wantSequence {
		t.Fatal("checkpoint load did not advance the observation clock")
	}
}

func TestStructuralCheckpointCooldownCoalescesAndCancels(t *testing.T) {
	store := checkpointTestStore(t)
	store.structure = checkpointTestGeneration(t, store, nil, 2)
	store.checkpoint.nextWrite = time.Now().Add(time.Hour)
	store.scheduleStructuralCheckpoint(t.Context())
	store.mu.Lock()
	first := store.checkpoint.done
	store.mu.Unlock()
	store.scheduleStructuralCheckpoint(t.Context())
	store.mu.Lock()
	duplicate := store.checkpoint.done != first
	done := store.drainStructuralCheckpointLocked()
	store.mu.Unlock()
	if duplicate {
		t.Fatal("duplicate checkpoint worker during cooldown")
	}
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("cooldown ignored cancellation")
	}
	if _, err := os.Stat(store.structureFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkpoint wrote during cooldown: %v", err)
	}
}

// installStructuralIncrement publishes one incremental generation holding names.
func installStructuralIncrement(t *testing.T, store *indexStore, id int64, names ...string) {
	t.Helper()
	builder, err := newStructuralBuilder(store, store.structure)
	testutil.FailErr(t, "create incremental builder", err)
	defer builder.close()
	index, _, err := builder.children(t.Context(), ".")
	testutil.FailErr(t, "open incremental directory", err)
	items := make([]pagedview.RangeItem[TreeItem], 0, len(names))
	for _, name := range names {
		item, err := builder.item(t.Context(), indexNode{path: name, name: name, regular: true})
		testutil.FailErr(t, "construct incremental item", err)
		items = append(items, item)
	}
	testutil.FailErr(t, "populate incremental directory", index.SetBatch(t.Context(), items))
	testutil.FailErr(t, "save incremental directory", builder.save(t.Context(), index,
		DirectoryObservation{Path: ".", Sequence: id, FirstListed: 1, Entries: len(names), Complete: true}))
	next, err := builder.seal(t.Context(), id)
	testutil.FailErr(t, "seal incremental directory", err)
	store.mu.Lock()
	store.initializeStructureLocked()
	store.installStructureLocked(next)
	store.mu.Unlock()
}

func rootItemPaths(t *testing.T, generation *structuralGeneration) []string {
	t.Helper()
	root, _, err := generation.directories.Get(t.Context(), ".")
	testutil.FailErr(t, "read generation root", err)
	index := pagedview.RangeIndex[TreeItem]{Store: generation, Root: root.page}
	extent, err := index.Extent(t.Context())
	testutil.FailErr(t, "read generation extent", err)
	paths := make([]string, 0, extent)
	for rank := int64(0); rank < extent; rank++ {
		item, err := index.SelectItem(t.Context(), rank)
		testutil.FailErr(t, "select generation item", err)
		paths = append(paths, item.Value.Path)
	}
	return paths
}

// Compaction absorbs concurrent publications to finish under sustained writes.
func TestStructuralCompactionAbsorbsHeadPublishedUnderTheCopy(t *testing.T) {
	store := checkpointTestStore(t)
	for generation := int64(1); generation <= 4; generation++ {
		installStructuralIncrement(t, store, generation, "a")
	}
	defer func() {
		store.mu.Lock()
		store.releaseCompletedStructureLocked()
		if store.structure != nil {
			store.structure.close()
			store.structure = nil
		}
		store.mu.Unlock()
	}()
	pin, err := store.retainGeneration(0, true)
	testutil.FailErr(t, "pin compaction source", err)
	defer pin.Release()
	pin.value.compactionDebtBytes = structuralCompactionDebtBytes
	pin.value.encodedBytes = 2 * structuralCompactionDebtBytes

	// Publication advances the head while compaction retains its source generation.
	installStructuralIncrement(t, store, 5, "a", "late")
	if store.structure.id != 5 {
		t.Fatalf("fixture head = %d, want 5", store.structure.id)
	}

	replacement, err := store.compactCheckpointPin(t.Context(), pin)
	testutil.FailErr(t, "compact past a moved head", err)
	if replacement == nil {
		t.Fatal("compaction abandoned its copy because head moved")
	}
	defer replacement.Release()
	if store.structure.id != 6 {
		t.Fatalf("compacted head = %d, want 6", store.structure.id)
	}
	if len(store.structure.segments) != 1 {
		t.Fatalf("compacted head segments = %d, want the chain collapsed to 1", len(store.structure.segments))
	}
	if paths := rootItemPaths(t, store.structure); len(paths) != 2 || paths[1] != "late" {
		t.Fatalf("compacted head lost the publication it absorbed: %v", paths)
	}
}

// Long chains trigger compaction even below the byte-debt threshold.
func TestStructuralCompactionDueOnSegmentCountWithoutDebt(t *testing.T) {
	generation := &structuralGeneration{segments: make(map[uint32]*structuralSegment)}
	for id := range structuralCompactionSegments - 1 {
		generation.segments[uint32(id+1)] = &structuralSegment{}
	}
	if structuralCompactionDue(generation) {
		t.Fatal("a chain under the ceiling asked for a rewrite")
	}
	generation.segments[uint32(structuralCompactionSegments)] = &structuralSegment{}
	if !structuralCompactionDue(generation) {
		t.Fatalf("a chain of %d segments did not earn a rewrite", len(generation.segments))
	}
	if generation.compactionDebtBytes != 0 {
		t.Fatal("fixture carried debt, so the segment ceiling was not what triggered")
	}
}
