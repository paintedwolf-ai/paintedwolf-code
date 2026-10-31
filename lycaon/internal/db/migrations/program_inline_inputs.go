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
		From:     Baseline{Revision: 9, Shape: "f415792ab6b06805d1093c35adea98f8ab6431cfbb33c588f3be9b11e6ac3a00"},
		To:       target, Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(programInlineInputsSQL))
			return err
		}}
}
