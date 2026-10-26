package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed program_inline_inputs.sql
var programInlineInputsSQL []byte

// ProgramInlineInputs keeps small screened child inputs in their durable
// output receipt, avoiding a separate filesystem claim for each tiny body.
func ProgramInlineInputs(target Baseline) Step {
	return Step{ID: "portable-program-inline-inputs", Source: programInlineInputsSQL,
		Checksum: "2f6787b9b0e76c555969117c561a220cc9218846e9fcc14df4646cc60c4c9e97",
		From:     Baseline{Revision: 6, Shape: "e3709277a435e4c75e8d5fbecfd321c0fe16617dd54caa490bbe50e32a168aa1"},
		To:       target, Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(programInlineInputsSQL))
			return err
		}}
}
