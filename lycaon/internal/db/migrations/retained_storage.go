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
	scratch, err := retainedSortBytes(ctx, database, pages*pageBytes, pageBytes)
	if err != nil {
		return p, err
	}
	p.scratchBytes, err = storageSum(p.scratchBytes, scratch)
	p.retainedStoragePrepared = err == nil
	return p, err
}

// retainedSortBytes covers the reviewed revision-4 route and retained-copy
// index builds. dbstat reads each source B-tree once, independent of how many
// snapshots share its rows. No migration or snapshot serialization runs here.
func retainedSortBytes(ctx context.Context, database DBTX, storeBytes, pageBytes uint64) (uint64, error) {
	cells, indexRecord, manifestRecord, manifestCells, err := retainedInventory(ctx, database)
	if err != nil {
		return 0, err
	}
	// The released shape has UNIQUE(snapshot_id,chunk_id). Retained copying
	// reconstructs that constraint before conversion, so each physical entry
	// occurs at most once per root, even when entry_count metadata is wrong.
	rootRows := manifestCells
	indexBytes, err := storageSum(storeBytes, storeBytes)
	if err != nil {
		return 0, err
	}
	indexRecord, err = storageSum(indexRecord, indexRecord, 80)
	if err != nil {
		return 0, err
	}
	manifestRecord, err = storageSum(manifestRecord, manifestRecord, 80)
	if err != nil {
		return 0, err
	}
	if rootRows != 0 && manifestRecord > math.MaxUint64/rootRows {
		return 0, fmt.Errorf("root sorter bound overflows")
	}
	// Existing index keys already occur in source pages. The new evidence key
	// takes disjoint JSON subtrees plus the source primary key; twice the source
	// file covers their encodings. Rechunked tables create indexes while empty;
	// their later index maintenance uses capped main pages, never bulk sorting.
	// Later populated index rebuilds concern unchanged source tables only.
	// This is specific to the registered v4 route.
	return sorterStorageBytes(max(indexBytes, rootRows*manifestRecord), max(cells, rootRows), max(indexRecord, manifestRecord), pageBytes)
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
