package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed live_model_outputs.go
var liveModelOutputsImplementation []byte

var liveModelOutputsRetirement = Retirement{
	Table: "live_model_outputs",
	Discard: "in-flight stream bytes that settlement and startup recovery cleared; " +
		"settled output is in model_outputs, and builds after 1.0.1 never wrote the table",
}

// LiveModelOutputs drops the streaming projection no build reads.
func LiveModelOutputs() Part {
	return Part{
		Source: liveModelOutputsImplementation,
		Writes: func(context.Context, DBTX, uint64) (uint64, error) { return 0, nil },
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			return DropReleased(ctx, tx, "source-manifests-and-command-records", liveModelOutputsRetirement)
		},
	}
}
