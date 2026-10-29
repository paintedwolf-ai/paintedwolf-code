package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/fspath"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type retainedCopyResult struct {
	sharedOriginal   bool
	wholeFile        bool
	preparedIdentity string
}

// A complete selection preserves every source byte, including shadow rows and
// counters. Recovery capture verifies the sealed bytes; exhaustive secondary
// index audits run after activation rather than delaying normal use.
func copyWholeRetainedStore(ctx context.Context, source, destination string, plan migrations.Plan) (retainedCopyResult, bool, error) {
	var result retainedCopyResult
	reader, err := sql.Open("sqlite", fileDSN(source, readerDSNQuery+"&_pragma=temp_store(file)&_pragma=threads(0)&_pragma=cache_size(-2000)"))
	if err != nil {
		return result, false, err
	}
	defer reader.Close()
	if err := checkUpgradeTempStorage(ctx, reader, plan.ScratchBytes()); err != nil {
		return result, true, err
	}
	complete, err := retentionComplete(ctx, reader, plan)
	if err != nil {
		if errors.Is(err, migrations.ErrIntegrity) {
			return result, true, err
		}
		if rebuildIntegrityFailure(err) {
			return result, false, nil
		}
		return result, false, err
	}
	if !complete {
		return result, false, nil
	}
	if err := requireSealedRebuild(source); err != nil {
		return result, false, nil
	}
	cloned, err := fileclone.Clone(source, destination)
	if err != nil {
		return result, true, err
	}
	if !cloned {
		info, err := os.Stat(source)
		if err != nil {
			return result, true, err
		}
		available, known, err := rebuildAvailableBytes(filepath.Dir(destination))
		if err != nil {
			return result, true, err
		}
		needed, err := storageSum(uint64(info.Size()), plan.ScratchBytes(), 64<<20)
		if err != nil {
			return result, true, err
		}
		if !known {
			return result, true, fmt.Errorf("whole-store copy capacity is unavailable")
		}
		if available < needed {
			return result, true, &InsufficientSpaceError{NeededBytes: needed, AvailableBytes: available}
		}
		if _, err := CopyQuiescentSnapshot(ctx, reader, source, destination); err != nil {
			return result, true, err
		}
	}
	result.sharedOriginal = cloned
	result.wholeFile = true
	err = prepareWholeRetainedStore(ctx, destination, plan, cloned)
	if rebuildIntegrityFailure(err) {
		// Confirmed damage encountered while preparing the image can use primary
		// reconstruction. Operational failures never trigger an expensive retry.
		return retainedCopyResult{}, false, os.Remove(destination)
	}
	if err != nil {
		return result, true, err
	}
	result.preparedIdentity, err = fspath.EntryIdentity(destination)
	return result, true, err
}

// EXCEPT compares primary-derived chunk and reference sets without nested
// full scans. The pinned merge and uniqueness sorts have measured disk bounds.
const retainedCompletenessSQL = `SELECT EXISTS(SELECT 1 FROM source_manifest_staging INDEXED BY sqlite_autoindex_source_manifest_staging_1),
 EXISTS(SELECT id FROM source_manifest_chunks NOT INDEXED EXCEPT SELECT chunk_id FROM source_snapshot_chunks INDEXED BY sqlite_autoindex_source_snapshot_chunks_1),
 COALESCE((SELECT SUM(entry_count) FROM source_manifest_chunks NOT INDEXED),0),
 EXISTS(SELECT 1 FROM source_snapshot_chunks INDEXED BY sqlite_autoindex_source_snapshot_chunks_1 GROUP BY snapshot_id,chunk_id HAVING count(*)>1)`

func retentionComplete(ctx context.Context, source DBTX, plan migrations.Plan) (bool, error) {
	entries, measured := plan.RetainedSourceEntries()
	if !measured {
		return false, nil
	}
	var staging, omitted, repeated bool
	var declared uint64
	err := source.QueryRowContext(ctx, retainedCompletenessSQL).Scan(&staging, &omitted, &declared, &repeated)
	if err == nil && repeated {
		return false, fmt.Errorf("%w: source snapshot repeats a physical chunk", migrations.ErrIntegrity)
	}
	return !staging && !omitted && declared == entries, err
}

func prepareWholeRetainedStore(ctx context.Context, path string, plan migrations.Plan, cloned bool) (err error) {
	writer, err := openRetainedUpgradeWriter(ctx, path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, writer.Close()) }()
	// Before recovery capture, existing-page writes may diverge from the
	// original clone. Later admission remeasures after this cost is consumed.
	storage, err := InspectUpgradeStorage(ctx, writer, path, true, cloned)
	if err != nil {
		return err
	}
	available, known, err := rebuildAvailableBytes(filepath.Dir(path))
	if err != nil {
		return err
	}
	if !known {
		return fmt.Errorf("whole-store preparation capacity is unavailable")
	}
	limit, err := storage.PageLimit(available, plan.ScratchBytes())
	if err != nil {
		return err
	}
	restore, err := limitUpgradePages(ctx, writer, limit)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, restore()) }()
	for _, statement := range plan.CopyPlan().After {
		if _, err := writer.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	actual, err := inspectSchema(ctx, writer)
	if err != nil {
		return err
	}
	if actual != plan.Source {
		return fmt.Errorf("whole-store copy changed the released schema")
	}
	return prepareAuditState(ctx, writer, true)
}

func rebuildIntegrityFailure(err error) bool {
	switch cause := err.(type) {
	case *StoreIncompatibleError:
		return cause.Reason == RecoveryReasonIntegrityFailed
	case *sqlite.Error:
		return cause.Code()&0xff == sqlite3.SQLITE_CORRUPT || cause.Code()&0xff == sqlite3.SQLITE_NOTADB
	case interface{ Unwrap() []error }:
		children := cause.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !rebuildIntegrityFailure(child) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return rebuildIntegrityFailure(cause.Unwrap())
	default:
		return err == migrations.ErrIntegrity
	}
}
