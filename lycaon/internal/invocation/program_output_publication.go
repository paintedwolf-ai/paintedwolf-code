package invocation

import "context"

const outputPublicationPageSize = 64

type ProgramOutputPublicationFile struct {
	ID, ExecutionID, Stream, Reference string
}

func (r *SQLRecorder) ProgramPublicationPending(ctx context.Context, session, program string) (bool, error) {
	var pending bool
	err := r.database.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM invocation_program_publications WHERE program_id=? AND session_id=? AND materialized=0
	)`, program, session).Scan(&pending)
	return pending, err
}

// ProgramOutputPublicationFiles reads scoped file copies from committed output.
func (r *SQLRecorder) ProgramOutputPublicationFiles(ctx context.Context, session, program, after string) ([]ProgramOutputPublicationFile, error) {
	rows, err := r.database.QueryContext(ctx, `
		SELECT o.id, COALESCE(o.execution_id,''), o.stream, o.reference
		FROM invocation_program_outputs o
		JOIN invocation_programs p ON p.id=o.program_id
		JOIN invocation_receipts i ON i.id=p.parent_receipt_id
		WHERE o.program_id=? AND i.session_id=? AND o.id>? AND o.stream IN ('stdout','stderr')`+publishedProgramOutput+`
		ORDER BY o.id LIMIT ?`, program, session, after, outputPublicationPageSize)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var files []ProgramOutputPublicationFile
	for rows.Next() {
		var file ProgramOutputPublicationFile
		if err := rows.Scan(&file.ID, &file.ExecutionID, &file.Stream, &file.Reference); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}
