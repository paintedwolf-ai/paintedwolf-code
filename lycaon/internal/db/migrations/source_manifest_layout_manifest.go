package migrations

import "context"

// manifestLayoutSource is the shipped revision-4 shape.
var manifestLayoutSource = Baseline{Revision: 4, Shape: "c0280798481d97c01e3ddf30812db8b6c28db69520cd8b33b88642ddb03d18a2"}

// SourceManifestLayout moves retained source snapshots onto per-root,
// content-defined manifests and drops the unreferenced bucket chunks.
func SourceManifestLayout(target Baseline) Step {
	return Step{
		ID: "source-manifest-layout", Source: append(append([]byte{}, manifestLayoutImplementation...), manifestLayoutSchema...),
		Checksum: "aa6b1ecee1065546bb8a4688dbd3e6b40acbdddd6b161ebe49bd407729775b88",
		From:     manifestLayoutSource, To: target, Apply: migrateManifestLayout, Validate: validateManifestLayout,
		Writes: manifestLayoutWrites,
	}
}

// Each retained entry is written once into the new tables with its index
// entries and passes through the sort that orders a root; dropping the old
// tables writes only free-list and pointer-map pages for what it frees.
const (
	manifestLayoutEntryBytes  = 1024
	manifestLayoutDropDivisor = 256
	manifestLayoutFixedBytes  = 64 << 20
)

func manifestLayoutWrites(ctx context.Context, store DBTX, storeBytes uint64) (uint64, error) {
	var retained int64
	if err := store.QueryRowContext(ctx, `SELECT COALESCE(SUM(file_count), 0) FROM source_snapshots`).Scan(&retained); err != nil {
		return 0, err
	}
	return uint64(max(retained, 0))*manifestLayoutEntryBytes + storeBytes/manifestLayoutDropDivisor + manifestLayoutFixedBytes, nil
}
