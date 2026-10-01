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
		return &InvalidError{Detail: "store.db is unreadable"}
	}
	plan, err := db.PlanUpgrade(ctx, source)
	if err == nil && plan.Required() {
		err = preflightUpgrade(ctx, CreateOpts{ConfigDir: root, DBPath: path, SQLDB: source}, plan.ScratchBytes(), false)
	}
	_ = source.Close()
	if err != nil {
		return &BaselineMismatchError{ArchiveVersion: manifest.SchemaUserVersion, BinaryVersion: db.SchemaVersion, ShapeDetail: err.Error()}
	}
	if plan.Source.Revision != manifest.SchemaUserVersion || plan.Source.Shape != manifest.SchemaShapeDigest {
		return &InvalidError{Detail: "store.db does not match its archive schema identity"}
	}
	if err := db.UpgradeStaged(ctx, path); err != nil {
		return fmt.Errorf("upgrade extracted history: %w", err)
	}
	if err := validateStagedStore(ctx, path, db.SchemaVersion); err != nil {
		return err
	}
	if err := historyretention.SuspendRestoredPolicy(root); err != nil {
		if errors.Is(err, historyretention.ErrInvalidPolicy) {
			return &InvalidError{Detail: "history-retention.json is invalid"}
		}
		return fmt.Errorf("suspend restored retention: %w", err)
	}
	for i := range manifest.Files {
		entry := &manifest.Files[i]
		if entry.RelPath != historyretention.PolicyFilename && (entry.RelPath != storeRelPath || !plan.Required()) {
			continue
		}
		sum, size, err := sourceDigest(ctx, archiveSource{path: filepath.Join(root, entry.RelPath), kind: fileKindRegular})
		if err != nil {
			return err
		}
		entry.SHA256, entry.Size = sum, size
		files[i].SHA256, files[i].Size = sum, size
	}
	manifest.SchemaUserVersion, manifest.SchemaShapeDigest = plan.Target.Revision, plan.Target.Shape
	return nil
}
