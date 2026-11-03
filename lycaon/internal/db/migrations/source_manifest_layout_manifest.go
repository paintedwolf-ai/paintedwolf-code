package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"math"
)

// manifestLayoutSource is the shipped revision-4 shape.
var manifestLayoutSource = Baseline{Revision: 4, Shape: "c0280798481d97c01e3ddf30812db8b6c28db69520cd8b33b88642ddb03d18a2"}

// SourceManifestLayout moves retained source snapshots onto per-root,
// content-defined manifests and drops the unreferenced bucket chunks.
func SourceManifestLayout(target Baseline) Step {
	return Step{
		ID: "source-manifest-layout", Source: bytes.Join([][]byte{manifestLayoutImplementation, manifestLayoutSchema, manifestLayoutConverterImplementation, manifestLayoutMemoImplementation}, nil),
		Checksum: "ae4d80d6bcf5cb51657e0b8d31648093917e22116b9e4e64b6c707643f1b42d1",
		From:     manifestLayoutSource, To: target, Apply: migrateManifestLayout, Validate: validateManifestLayout,
		Writes:       manifestLayoutWrites,
		RetainedCopy: retainedManifestCopy(),
	}
}

func retainedManifestCopy() *RetainedCopy {
	return &RetainedCopy{
		SourceShapes: []string{SourceNamespace().From.Shape, sourceNamespaceTarget.Shape, manifestLayoutSource.Shape},
		// Independent cardinalities: source chunk metadata states how many entries
		// must survive, rather than trusting the copying predicate to validate itself.
		ExpectedRows: map[string]string{
			"source_manifest_chunks":  `SELECT COUNT(DISTINCT chunk_id) FROM original.source_snapshot_chunks NOT INDEXED`,
			"source_manifest_entries": `SELECT COALESCE(SUM(entry_count),0) FROM original.source_manifest_chunks NOT INDEXED WHERE id IN (SELECT chunk_id FROM original.source_snapshot_chunks NOT INDEXED)`,
			"source_manifest_staging": `SELECT 0`,
		},
		Sources: map[string]string{
			"source_manifest_chunks": `FROM (SELECT DISTINCT chunk_id FROM original.source_snapshot_chunks NOT INDEXED) c
CROSS JOIN original.source_manifest_chunks e ON e.id = c.chunk_id`,
			"source_manifest_entries": `FROM (SELECT DISTINCT chunk_id FROM original.source_snapshot_chunks NOT INDEXED) c
CROSS JOIN original.source_manifest_entries e ON e.chunk_id = c.chunk_id`,
			"source_manifest_staging": `FROM original.source_manifest_staging e WHERE 0`,
		},
		// Reclamation checks surviving references independently. Reading every
		// discarded entry just to discover candidates would defeat the rebuild.
		After: []string{`INSERT OR IGNORE INTO source_blob_reclaim_queue SELECT sha256 FROM source_blob_objects`},
	}
}

// validateManifestLayout requires every retained snapshot to list exactly
// the files it counted, through one manifest per root.
func validateManifestLayout(ctx context.Context, tx *sql.Tx) error {
	var mismatched int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM source_snapshots s
WHERE s.file_count != COALESCE((
    SELECT SUM(m.entry_count) FROM source_snapshot_roots r JOIN source_manifests m ON m.id = r.manifest_id
    WHERE r.snapshot_id = s.id), 0)`).Scan(&mismatched); err != nil {
		return err
	}
	if mismatched != 0 {
		return fmt.Errorf("%d retained snapshots lost entries in the manifest layout", mismatched)
	}
	return nil
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
