package migrations

import (
	"context"
	"database/sql"
	_ "embed"
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

var releasedRevision5 = Baseline{Revision: 5, Shape: "a1c252d46e94940750d9b239e96716d7f8d3a612fb1a2d24969085876aac2aaa"}

// Revision5 upgrades to the released manifest and command-record schema.
func Revision5() Step {
	return composeStep(Step{
		ID:       "source-manifests-and-command-records",
		Checksum: "8e1afb6494bcc70516ccbb226a0128d9d2d566bac1d05a829ed98aa7daeb93ae",
		From:     manifestLayoutSource, To: releasedRevision5,
	}, SourceManifestLayout(), VaultUnlockAway(), EffectPermitIdentity(), ProgramReceipts(), SourceLinkKind(), SessionProgressIdentity(), TurnLoadVisibility(), EvidenceRootID(), ToolResultFileEdits(), Briefings(), LiveModelOutputs(), WorkerRecords())
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

//go:embed briefings.sql
var briefingsSQL []byte

// Briefings adds content-addressed briefing results, records and targets.
func Briefings() Part {
	return Part{Source: briefingsSQL, Apply: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, string(briefingsSQL))
		return err
	}}
}
