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
		Checksum: "25c49ce1ad22f336ff4f77dac47f314e36bd733c1594712aa10c949bcef38dba",
		From:     Baseline{Revision: 6, Shape: "43e05af10021aa35c68465be04ca4b7a0682467126431090f084be51ad749184"},
		To:       target, Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(programInlineInputsSQL))
			return err
		}}
}
