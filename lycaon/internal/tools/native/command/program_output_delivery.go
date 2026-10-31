package command

import (
	"context"
	"errors"
	"io"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	largeContextOutputBudgetBytes  = 64 << 10
	mediumContextOutputBudgetBytes = 32 << 10
	smallContextOutputBudgetBytes  = 12 << 10
)

// CommandOutputBudget returns the total model-boundary output budget in bytes
// for a given model context window in tokens.
func CommandOutputBudget(contextTokens int) int {
	switch {
	case contextTokens > 0 && contextTokens <= 32768:
		return smallContextOutputBudgetBytes
	case contextTokens > 32768 && contextTokens < 128000:
		return mediumContextOutputBudgetBytes
	default:
		return largeContextOutputBudgetBytes
	}
}

func splitStreamBudgets(totalBudget, stdoutBytes, stderrBytes int) (stdoutBudget, stderrBudget int) {
	switch {
	case stdoutBytes > 0 && stderrBytes > 0:
		stdoutBudget = totalBudget / 2
		stderrBudget = totalBudget - stdoutBudget
	case stdoutBytes > 0:
		stdoutBudget = totalBudget
	case stderrBytes > 0:
		stderrBudget = totalBudget
	default:
		stdoutBudget = totalBudget
		stderrBudget = totalBudget
	}
	return stdoutBudget, stderrBudget
}

func (r *ProgramRuntime) outcome(ctx context.Context, run *programRun, outputs *invocation.ProgramOutputRetention, result toolcontract.ProducerResult, stdout, stderr *programStream, scope captureprojection.Scope) (ProgramOutcome, error) {
	projection, err := r.Recorder.ProjectProgram(ctx, run.tc.Identity.SessionID, run.root.Identity.ProgramID, 4)
	if err != nil {
		return ProgramOutcome{}, err
	}
	if run.tc.Effects.Out != nil {
		receipt := projection.Program
		run.tc.Effects.Out.CommandProgram = &receipt
	}
	programID := run.root.Identity.ProgramID
	out := ProgramOutcome{
		ProgramID:        programID,
		State:            projection.Program.State,
		Status:           result.Status,
		StopCode:         result.StopCode,
		ExecutedChildren: projection.ExecutedChildren,
		CompletedEffects: projection.CompletedEffects,
		UncertainEffects: projection.UncertainEffects,
		NativeGaps:       projection.NativeGaps,
	}

	stdoutBudget, stderrBudget := splitStreamBudgets(CommandOutputBudget(run.tc.Host.ModelContextWindow), int(stdout.Len()), int(stderr.Len()))
	for i, stream := range []*programStream{stdout, stderr} {
		if stream.Len() == 0 {
			continue
		}
		name, budget := "stdout", stdoutBudget
		if i == 1 {
			name, budget = "stderr", stderrBudget
		}
		delivered, window, err := r.deliverProgramStream(ctx, run, outputs, stream, name, budget, scope)
		if err != nil {
			return ProgramOutcome{}, err
		}
		if i == 0 {
			out.Stdout, out.StdoutRef = delivered, window.Ref
		} else {
			out.Stderr, out.StderrRef = delivered, window.Ref
		}
		out.Outputs = append(out.Outputs, window)
		out.Truncated = out.Truncated || window.Truncated
	}
	if run.tc.Effects.Out != nil && run.tc.Effects.Out.CommandProgram != nil {
		run.tc.Effects.Out.CommandProgram.Outputs = append([]api.CommandProgramOutputWindow(nil), out.Outputs...)
	}
	return out, nil
}

// retainProgramStream crosses durable screening once and gives its emitted
// stream directly to the invocation retention owner.
func (r *ProgramRuntime) retainProgramStream(ctx context.Context, run *programRun, stream *programStream, name string, scope captureprojection.Scope) (*tooloutput.StreamSpill, bool, error) {
	type retained struct {
		spill *tooloutput.StreamSpill
		err   error
	}
	done := make(chan retained, 1)
	raw, _, err := stream.snapshotInput()
	if err != nil {
		return nil, false, err
	}
	input, output := io.Pipe()
	maximum := stream.limit
	go func() {
		spill, err := tooloutput.RetainScreenedStream(ctx, tooloutput.ScreenedReader(input), tooloutput.StreamSpillOptions{
			Store: r.Recorder.ProgramBlobStore(ctx, r.Blobs), ClaimID: run.root.Identity.ProgramID + ":" + name,
			MaximumBytes: maximum, PreviewBytes: 4 << 10, Admission: r.sessionResources(scope.SessionID), WorkingMemory: r.sessionResources(scope.SessionID), RetainedState: r.sessionResources(scope.SessionID),
		})
		_ = input.CloseWithError(err)
		done <- retained{spill, err}
	}()
	metadata, screenErr := r.Projector.ScreenStream(ctx, scope, "capture.native", raw, output, captureprojection.StreamOptions{
		WorkingMemory: run.memory,
		Bounds:        secretmatch.StreamBounds{InputBytes: maximum, OutputBytes: maximum, MemoryBytes: 32 << 20, DetectorWork: 1 << 30},
	})
	_ = output.CloseWithError(screenErr)
	result := <-done
	if result.err != nil {
		return nil, false, result.err
	}
	if result.spill != nil && outputDeliveryLimit(result.spill.Failure) != nil {
		return result.spill, metadata.Redacted, nil
	}
	return result.spill, metadata.Redacted, screenErr
}

func (r *ProgramRuntime) deliverProgramStream(ctx context.Context, run *programRun, outputs *invocation.ProgramOutputRetention, stream *programStream, name string, budget int, scope captureprojection.Scope) (string, api.CommandProgramOutputWindow, error) {
	spill, redacted, err := r.retainProgramStream(ctx, run, stream, name, scope)
	if err != nil {
		if spill != nil {
			_ = spill.Close()
		}
		return "", api.CommandProgramOutputWindow{}, err
	}
	defer func() { _ = spill.Close() }()
	program := run.root.Identity.ProgramID
	reference := "output:program:" + program + ":" + name
	outputPath := "@output/" + program + "." + name
	body, err := spill.Open(ctx)
	if err != nil {
		return "", api.CommandProgramOutputWindow{}, err
	}
	err = scratch.PublishCommandOutput(run.tc.Host.SessionOutputDir, program, name, body)
	closeErr := body.Close()
	if err != nil || closeErr != nil {
		return "", api.CommandProgramOutputWindow{}, errors.Join(err, closeErr)
	}
	body, err = spill.Open(ctx)
	if err != nil {
		return "", api.CommandProgramOutputWindow{}, err
	}
	releaseSample, err := run.memory.Reserve(ctx, int64(2*budget+64<<10))
	if err != nil {
		_ = body.Close()
		return "", api.CommandProgramOutputWindow{}, err
	}
	defer releaseSample()
	sample, err := sampleOutput(body, budget)
	closeErr = body.Close()
	if err != nil || closeErr != nil {
		return "", api.CommandProgramOutputWindow{}, errors.Join(err, closeErr)
	}
	delivered, window, ranges, err := sample.window(ctx, name, reference, outputPath, !stream.truncated && spill.Complete)
	if err != nil {
		return "", api.CommandProgramOutputWindow{}, err
	}
	window.Limit = outputDeliveryLimit(spill.Failure)
	var mappings []toolcontract.StreamSourceMapping
	if !redacted && !stream.truncated && spill.Complete {
		snapshot, err := stream.snapshotMappings(ctx, spill.Blob.Size)
		if err == nil && snapshot != nil {
			resolved := r.resolveOutputMappings(ctx, run, snapshot.Mappings, spill.Blob.Size)
			_ = snapshot.Close()
			defer resolved.Close()
			mappings = resolved.values
		}
	}
	retained := invocation.RetainedOutput{Stream: name, Reference: reference, Complete: !stream.truncated && spill.Complete, Redacted: redacted, Mappings: mappings}
	mappings, err = outputs.Attach(ctx, program, retained, spill)
	if err != nil {
		return "", api.CommandProgramOutputWindow{}, err
	}
	if err := outputs.AttachDeliveryWindow(ctx, program, window); err != nil {
		return "", api.CommandProgramOutputWindow{}, err
	}
	for _, part := range ranges {
		r.commitStreamSourceDeliveries(ctx, run, program, reference, part.data, mappings, [][2]int64{{part.start, part.start + int64(len(part.data))}})
	}
	return delivered, window, nil
}
