package db

import (
	"context"
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
	sharedOriginal    bool
	wholeFile         bool
	validatedIdentity string
}

// A complete selection preserves every source byte, including shadow rows and
// counters. Full validation of this same image establishes index trust before
// the candidate predicates can authorize recovery capture.
func copyWholeRetainedStore(ctx context.Context, source, destination string, plan migrations.Plan, progress func(migrations.Phase)) (retainedCopyResult, bool, error) {
	var result retainedCopyResult
	reader, err := openReader(ctx, source)
	if err != nil {
		return result, false, err
	}
	defer reader.Close()
	complete, err := retentionComplete(ctx, reader, plan)
	if err != nil {
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
	err = prepareWholeRetainedStore(ctx, destination, plan, cloned, progress)
	if rebuildIntegrityFailure(err) {
		// A damaged secondary index can be reconstructed from primary rows by
		// the existing path. Operational failures never trigger an expensive retry.
		return retainedCopyResult{}, false, os.Remove(destination)
	}
	if err != nil {
		return result, true, err
	}
	result.validatedIdentity, err = fspath.EntryIdentity(destination)
	return result, true, err
}

func retentionComplete(ctx context.Context, source DBTX, plan migrations.Plan) (bool, error) {
	entries, measured := plan.RetainedSourceEntries()
	if !measured {
		return false, nil
	}
	var staging, omitted bool
	var declared uint64
	err := source.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_manifest_staging NOT INDEXED),
 EXISTS(SELECT 1 FROM source_manifest_chunks m NOT INDEXED WHERE NOT EXISTS(SELECT 1 FROM source_snapshot_chunks c WHERE c.chunk_id=m.id)),
 COALESCE((SELECT SUM(entry_count) FROM source_manifest_chunks NOT INDEXED),0)`).Scan(&staging, &omitted, &declared)
	return !staging && !omitted && declared == entries, err
}

func prepareWholeRetainedStore(ctx context.Context, path string, plan migrations.Plan, cloned bool, progress func(migrations.Phase)) (err error) {
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
	if progress != nil {
		progress(migrations.Validating)
	}
	return validateUpgradeIntegrity(ctx, writer)
}

func rebuildIntegrityFailure(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !rebuildIntegrityFailure(child) {
				return false
			}
		}
		return true
	}
	var damage *StoreIncompatibleError
	if errors.As(err, &damage) && damage.Reason == RecoveryReasonIntegrityFailed {
		return true
	}
	var failure *sqlite.Error
	return errors.Is(err, migrations.ErrIntegrity) || (errors.As(err, &failure) && (failure.Code()&0xff == sqlite3.SQLITE_CORRUPT || failure.Code()&0xff == sqlite3.SQLITE_NOTADB))
}
