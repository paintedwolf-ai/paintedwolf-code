package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed 002_workflow_provenance.sql
var workflowProvenanceSQL []byte

// WorkflowProvenance preserves released stores while adding run asset provenance.
func WorkflowProvenance() Step {
	return Step{
		ID: "002_workflow_provenance", Source: workflowProvenanceSQL,
		Checksum:     "420c542962ed4087abbef04ddba823d7e3b4d0e659f8d8a020867b7886d3c4ac",
		From:         Baseline{Revision: 1, Shape: "4c183bce0a7e107596c03a44a68b384cf216b3932ddf31e17d95f400772f478a"},
		To:           Baseline{Revision: 2, Shape: "4a6d6c8e4318c30c52842e706d0897656903150dea0942509bda8720a52b7de7"},
		ScratchBytes: 1 << 20,
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(workflowProvenanceSQL))
			return err
		},
	}
}
