package backup

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/historyretention"
)

func upgradeStagedStore(ctx context.Context, root string, manifest *Manifest, files []PendingFile) error {
	path := filepath.Join(root, storeRelPath)
	source, err := db.OpenReadOnly(ctx, path)
	if err != nil {
		return stagedInspectionFailure(err, manifest, "open extracted history")
	}
	plan, err := db.PlanUpgrade(ctx, source)
	defer func() { _ = source.Close() }()
	if err != nil {
		return stagedInspectionFailure(err, manifest, "inspect extracted history")
	}
	if plan.Required() {
		if err := preflightUpgrade(ctx, CreateOpts{ConfigDir: root, DBPath: path, SQLDB: source}, plan, false); err != nil {
			return err
		}
	}
	if plan.Source.Revision != manifest.SchemaUserVersion || plan.Source.Shape != manifest.SchemaShapeDigest {
		return &InvalidError{Detail: "store.db does not match its archive schema identity"}
	}
	_ = source.Close()
	if err := db.UpgradeStaged(ctx, path); err != nil {
		return fmt.Errorf("upgrade extracted history: %w", err)
	}
	if err := historyretention.SuspendRestoredPolicy(root); err != nil {
		if errors.Is(err, historyretention.ErrInvalidPolicy) {
			return &InvalidError{Detail: "history-retention.json is invalid"}
		}
		return fmt.Errorf("suspend restored retention: %w", err)
	}
	for i := range manifest.Files {
		entry := &manifest.Files[i]
		if entry.RelPath != historyretention.PolicyFilename && entry.RelPath != storeRelPath {
			continue
		}
		sum, size, err := sourceDigest(ctx, archiveSource{path: filepath.Join(root, entry.RelPath), kind: fileKindRegular})
		if err != nil {
			return err
		}
		entry.SHA256, entry.Size = sum, size
		if i < len(files) {
			files[i].SHA256, files[i].Size = sum, size
		}
	}
	manifest.SchemaUserVersion, manifest.SchemaShapeDigest = plan.Target.Revision, plan.Target.Shape
	return nil
}

func stagedInspectionFailure(err error, manifest *Manifest, operation string) error {
	classified := db.ClassifyStoreFailure(err)
	var failure *db.StoreIncompatibleError
	if errors.As(classified, &failure) {
		switch failure.Reason {
		case db.RecoveryReasonInsufficientSpace, db.RecoveryReasonStorageUnavailable:
			return fmt.Errorf("%s: %w", operation, classified)
		case db.RecoveryReasonIntegrityFailed:
			return &InvalidError{Detail: "store.db is not a readable SQLite database"}
		case db.RecoveryReasonSchemaMismatch:
			return &BaselineMismatchError{ArchiveVersion: manifest.SchemaUserVersion, BinaryVersion: db.SchemaVersion, ShapeDetail: failure.Detail}
		}
	}
	return fmt.Errorf("%s: %w", operation, classified)
}
