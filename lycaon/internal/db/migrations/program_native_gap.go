package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed program_native_gap.sql
var programNativeGapSQL []byte

// ProgramNativeGap retains a native fallback fact on its actual execution.
func ProgramNativeGap(target Baseline) Step {
	return Step{ID: "portable-program-native-gap", Source: programNativeGapSQL,
		Checksum: "f19c6276d3a1d4e7693db5fcb5d4261d46c3033b2878117c06e0f04b55998540",
		From:     Baseline{Revision: 8, Shape: "20ea647f27e29c4af869abf276549fb66cb94354d055b9dd6cf15e942d3f8635"},
		To:       target, Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(programNativeGapSQL))
			return err
		}}
}
