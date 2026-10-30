package migrations

import (
	"context"
	"fmt"
	"math"
)

// manifestLayoutSource is the shipped revision-4 shape.
var manifestLayoutSource = Baseline{Revision: 4, Shape: "c0280798481d97c01e3ddf30812db8b6c28db69520cd8b33b88642ddb03d18a2"}

// SourceManifestLayout moves retained source snapshots onto per-root,
// content-defined manifests and drops the unreferenced bucket chunks.
func SourceManifestLayout(target Baseline) Step {
	return Step{
		ID: "source-manifest-layout", Source: append(append([]byte{}, manifestLayoutImplementation...), manifestLayoutSchema...),
		Checksum: "3af0f1899c7ec5e85633c260f16d75af8ed506f0cd888e60fd096dcfc54e91d3",
		From:     manifestLayoutSource, To: target, Apply: migrateManifestLayout, Validate: validateManifestLayout,
		Writes:       manifestLayoutWrites,
		RetainedCopy: retainedManifestCopy(),
	}
}

// Budget retained rows, their indexes and sort copies using their actual
// encoded text lengths. Dropping old tables writes free-list and pointer-map pages.
const (
	manifestLayoutEntryBytes  = 1024
	manifestLayoutDropDivisor = 256
	manifestLayoutFixedBytes  = 64 << 20
)

func manifestLayoutWrites(ctx context.Context, store DBTX, storeBytes uint64) (uint64, error) {
	var entries, entryText, roots, rootText int64
	if err := store.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(
 length(CAST(e.path AS BLOB)) + length(CAST(e.sha256 AS BLOB)) +
 length(CAST(e.git_oid AS BLOB)) + length(CAST(e.identity AS BLOB))), 0)
 FROM source_snapshot_chunks c
 CROSS JOIN source_manifest_entries e ON e.chunk_id = c.chunk_id
 JOIN source_snapshot_roots r ON r.snapshot_id = c.snapshot_id AND r.root_path = e.root_path`).Scan(&entries, &entryText); err != nil {
		return 0, err
	}
	if err := store.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(length(CAST(root_path AS BLOB)) + length(CAST(snapshot_id AS BLOB))), 0) FROM source_snapshot_roots`).Scan(&roots, &rootText); err != nil {
		return 0, err
	}
	total := storeBytes/manifestLayoutDropDivisor + manifestLayoutFixedBytes
	for _, part := range []struct {
		count int64
		scale uint64
	}{
		{entries, manifestLayoutEntryBytes}, {entryText, 4}, {roots, manifestLayoutEntryBytes}, {rootText, 4},
	} {
		if part.count < 0 || uint64(part.count) > (math.MaxUint64-total)/part.scale {
			return 0, fmt.Errorf("manifest migration write estimate overflows")
		}
		total += uint64(part.count) * part.scale
	}
	return total, nil
}
