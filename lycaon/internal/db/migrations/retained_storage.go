package migrations

import (
	"context"
	"fmt"
	"math"
)

// PrepareRetainedStorage binds the reviewed v4 route's temporary-work bound
// to its source once, before copying or recovery capture. Other routes retain
// their registered resource declarations.
func (p Plan) PrepareRetainedStorage(ctx context.Context, database DBTX) (Plan, error) {
	if p.retainedStoragePrepared || !p.reviewedRetainedRoute() {
		return p, nil
	}
	var pages, pageBytes uint64
	if err := database.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return p, err
	}
	if err := database.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageBytes); err != nil {
		return p, err
	}
	if pageBytes == 0 || pages > math.MaxUint64/pageBytes {
		return p, fmt.Errorf("invalid retained source size")
	}
	trees, err := retainedInventory(ctx, database, nil, true)
	if err != nil {
		return p, err
	}
	scratch, err := retainedRouteSortBytes(trees, pageBytes)
	if err != nil {
		return p, err
	}
	p.scratchBytes, err = storageSum(p.scratchBytes, scratch)
	p.retainedStoragePrepared = err == nil
	p.retainedEntryRows = trees["source_manifest_entries"].cells
	p.retainedTrees = trees
	p.retainedStoreBytes = pages * pageBytes
	p.retainedPageBytes = pageBytes
	return p, err
}

// RetainedCopyScratch surveys old indexes only when reconstruction is needed.
// It reuses primary facts already measured for the migration itself.
func (p Plan) RetainedCopyScratch(ctx context.Context, database DBTX) (uint64, error) {
	if !p.BoundedRetainedStorage() {
		return 0, nil
	}
	trees, err := retainedInventory(ctx, database, p.retainedTrees, false)
	if err != nil {
		return 0, err
	}
	var rows, largest uint64
	for _, tree := range trees {
		if tree.kind == "table" {
			rows, err = storageSum(rows, tree.cells)
			if err != nil {
				return 0, err
			}
		}
		if tree.kind == "index" || tree.name == "evidence_records" || tree.name == "source_snapshot_roots" {
			largest = max(largest, tree.record)
		}
	}
	records, err := storageSum(p.retainedStoreBytes, p.retainedStoreBytes)
	if err != nil {
		return 0, err
	}
	largest, err = storageSum(largest, largest, 80)
	if err != nil {
		return 0, err
	}
	scratch, err := sorterStorageBytes(records, rows, largest, p.retainedPageBytes)
	return max(scratch, p.ScratchBytes()), err
}

// Each class has its own record-size bound. A large checkpoint payload does
// not inflate every manifest sort's merge window. No amplified target table
// receives a later bulk index: its empty indexes are maintained in main pages.
func retainedRouteSortBytes(trees map[string]retainedTree, pageBytes uint64) (uint64, error) {
	var indexRows, indexBytes, indexRecord uint64
	for _, name := range []string{"vault_unlocks", "evidence_records", "source_files", "source_versions", "source_head_entries", "source_effects"} {
		tree := trees[name]
		var err error
		indexRows, err = storageSum(indexRows, tree.cells)
		if err != nil {
			return 0, err
		}
		indexBytes, err = storageSum(indexBytes, tree.bytes, tree.bytes)
		if err != nil {
			return 0, err
		}
		indexRecord = max(indexRecord, tree.record)
	}
	largest, err := storageSum(indexRecord, indexRecord, 80)
	if err != nil {
		return 0, err
	}
	budget, err := sorterStorageBytes(indexBytes, indexRows, largest, pageBytes)
	if err != nil {
		return 0, err
	}
	// UNIQUE(snapshot_id,chunk_id) is re-enforced before conversion: a root
	// selects each physical manifest row at most once, independent of metadata.
	for _, name := range []string{"source_manifest_entries", "source_snapshot_roots", "sqlite_schema"} {
		tree := trees[name]
		largest, err = storageSum(tree.record, tree.record, 80)
		if err != nil {
			return 0, err
		}
		if tree.cells != 0 && largest > math.MaxUint64/tree.cells {
			return 0, fmt.Errorf("retained sort input overflows")
		}
		bytes, err := sorterStorageBytes(tree.cells*largest, tree.cells, largest, pageBytes)
		if err != nil {
			return 0, err
		}
		budget = max(budget, bytes)
	}
	// The pinned EXCEPT merge opens one chunk-ID and one reference sorter;
	// duplicate-reference validation opens a second reference sorter. Reserve
	// all three together, even if their scalar subqueries finish sequentially.
	var selection uint64
	for _, name := range []string{"source_manifest_chunks", "source_snapshot_chunks", "source_snapshot_chunks"} {
		tree := trees[name]
		largest, err := storageSum(tree.record, tree.record, 80)
		if err != nil || tree.cells != 0 && largest > math.MaxUint64/tree.cells {
			return 0, fmt.Errorf("retained selection input overflows")
		}
		bytes, err := sorterStorageBytes(tree.cells*largest, tree.cells, largest, pageBytes)
		if err != nil {
			return 0, err
		}
		selection, err = storageSum(selection, bytes)
		if err != nil {
			return 0, err
		}
	}
	budget = max(budget, selection)
	// The authority conversion pages at most32 checkpoint payloads by id. If
	// SQLite chooses a bounded ORDER BY index, its ephemeral tree has at most33
	// records including the in-flight key. Charge the maximum legal temporary
	// page size, independently of the main database geometry.
	largest, err = storageSum(trees["checkpoints"].record, trees["checkpoints"].record, 80)
	if err != nil {
		return 0, err
	}
	if largest > math.MaxUint64/66 {
		return 0, fmt.Errorf("checkpoint sort input overflows")
	}
	checkpoint, err := storageSum(66*largest, 66*65536, 64<<20)
	return max(budget, checkpoint), err
}

// sorterStorageBytes follows the pinned SQLite single-threaded PMA sorter:
// length/run varints use <=18 bytes per row; list records add16 bytes and up to7 bytes of ROUND8 padding. Runs
// merge through a16-way tree, with one bounded file2 window per merge node.
func sorterStorageBytes(records, rows, largest, pageBytes uint64) (uint64, error) {
	if pageBytes < 512 || pageBytes > 65536 || pageBytes&(pageBytes-1) != 0 {
		return 0, fmt.Errorf("invalid sorter page size")
	}
	if rows > math.MaxUint64/23 {
		return 0, fmt.Errorf("sorter row bound overflows")
	}
	framed, err := storageSum(records, 18*rows)
	if err != nil {
		return 0, err
	}
	list, err := storageSum(records, 23*rows)
	if err != nil {
		return 0, err
	}
	minimum := 250 * pageBytes
	runs := min(rows, 2*(list/minimum)+2)
	// Two adjacent runs account for more than the minimum list allocation.
	// Include the root even when the final small merge remains in memory.
	nodes := uint64(1)
	for runs > 1 {
		runs = (runs + 15) / 16
		nodes, err = storageSum(nodes, runs)
		if err != nil {
			return 0, err
		}
	}
	window, err := storageSum(largest, 18)
	if err != nil {
		return 0, err
	}
	window = max(window, max(minimum, uint64(2000*1024))/2)
	if nodes > math.MaxUint64/window {
		return 0, fmt.Errorf("sorter merge bound overflows")
	}
	return storageSum(framed, nodes*window, 64<<20)
}

func storageSum(values ...uint64) (uint64, error) {
	var out uint64
	for _, v := range values {
		if v > math.MaxUint64-out {
			return 0, fmt.Errorf("upgrade storage bound overflows")
		}
		out += v
	}
	return out, nil
}

// RetainedSourceEntries is the measured primary-row count for the frozen v4 shape.
func (p Plan) RetainedSourceEntries() (uint64, bool) {
	return p.retainedEntryRows, p.BoundedRetainedStorage()
}
