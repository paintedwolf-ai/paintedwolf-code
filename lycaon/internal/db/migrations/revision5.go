package migrations

import (
	"context"
	"database/sql"
	"fmt"
)

// Part is one ordered transformation inside a registered step.
type Part struct {
	Source        []byte
	RebuildTables bool
	Apply         func(context.Context, *sql.Tx) error
	Validate      func(context.Context, *sql.Tx) error
	RetainedCopy  *RetainedCopy
	// Writes bounds the bytes the part writes. A part without it may rewrite
	// the whole store.
	Writes func(ctx context.Context, store DBTX, storeBytes uint64) (uint64, error)
}

// Revision5 is the single step from the shipped revision-4 shape. Until a build
// holding revision 5 is published, release candidates included, schema changes
// join these parts instead of advancing SchemaVersion.
func Revision5(target Baseline) Step {
	return composeStep(Step{
		ID:       "source-manifests-and-command-records",
		Checksum: "c5c2979a6e4b34c0e52a5538c5171c01fc022ddd48f6c13b3d605254abe8744f",
		From:     manifestLayoutSource, To: target,
	}, SourceManifestLayout(), VaultUnlockAway(), EffectPermitIdentity(), ProgramReceipts(), SourceLinkKind(), SessionProgressIdentity(), TurnLoadVisibility(), EvidenceRootID(), ToolResultFileEdits())
}

func composeStep(step Step, parts ...Part) Step {
	for _, part := range parts {
		step.Source = append(step.Source, part.Source...)
		step.RebuildTables = step.RebuildTables || part.RebuildTables
		if part.RetainedCopy != nil {
			step.RetainedCopy = part.RetainedCopy
		}
	}
	step.Apply = func(ctx context.Context, tx *sql.Tx) error {
		for _, part := range parts {
			if err := part.Apply(ctx, tx); err != nil {
				return err
			}
			if part.Validate != nil {
				if err := part.Validate(ctx, tx); err != nil {
					return err
				}
			}
		}
		return nil
	}
	step.Writes = func(ctx context.Context, store DBTX, storeBytes uint64) (uint64, error) {
		var total uint64
		for _, part := range parts {
			written := storeBytes
			if part.Writes != nil {
				var err error
				if written, err = part.Writes(ctx, store, storeBytes); err != nil {
					return 0, err
				}
			}
			if written > ^uint64(0)-total {
				return 0, fmt.Errorf("migration write estimate overflows")
			}
			total += written
		}
		return total, nil
	}
	return step
}
