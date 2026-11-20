package command

import (
	"context"
	"errors"
	"log/slog"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/scratch"
)

// RecoverProgramPublications replays sealed output publication after restart.
func (r *ProgramRuntime) RecoverProgramPublications(ctx context.Context, outputDir func(context.Context, string) (string, error)) error {
	const batchSize = 64
	var after string
	for {
		keys, err := r.Provenance.ListRecoverableProgramPublications(ctx, after, batchSize)
		if err != nil {
			return err
		}
		for _, key := range keys {
			if err := r.recoverProgramPublication(ctx, key, outputDir); err != nil {
				slog.WarnContext(ctx, "command output publication remains pending", "program_id", key.ProgramID, "error", err)
			}
			after = key.ProgramID
		}
		if len(keys) < batchSize {
			return nil
		}
	}
}

func (r *ProgramRuntime) recoverProgramPublication(ctx context.Context, key evidence.ProgramPublicationKey, outputDir func(context.Context, string) (string, error)) error {
	if err := r.Provenance.CommitProgramPublication(ctx, key.SessionID, key.ProgramID); err != nil {
		return err
	}
	dir, err := outputDir(ctx, key.SessionID)
	if err != nil {
		return err
	}
	outputs, err := r.sessionOutputs(ctx, key.SessionID)
	if err != nil {
		return err
	}
	if err := r.materializeProgramOutputs(ctx, outputs, key.SessionID, key.ProgramID, dir); err != nil {
		return err
	}
	return r.Provenance.MarkProgramPublicationMaterialized(ctx, key.SessionID, key.ProgramID)
}

func (r *ProgramRuntime) materializeProgramOutputs(ctx context.Context, outputs *invocation.ProgramOutputRetention, session, program, outputDir string) error {
	var after string
	for {
		files, err := r.Recorder.ProgramOutputPublicationFiles(ctx, session, program, after)
		if err != nil || len(files) == 0 {
			return err
		}
		for _, file := range files {
			body, err := outputs.Open(ctx, session, program, file.Reference)
			if err != nil {
				return err
			}
			output := scratch.CommandOutput{ProgramID: program, ExecutionID: file.ExecutionID, Stream: file.Stream}
			publishErr := scratch.PublishCommandOutput(outputDir, output, body.Reader)
			if err := errors.Join(publishErr, body.Reader.Close()); err != nil {
				return err
			}
			after = file.ID
		}
	}
}
