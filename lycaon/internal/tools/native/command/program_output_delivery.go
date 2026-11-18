package command

import (
	"context"
	"errors"
	"io"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/evidence"
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
	programID := run.root.Identity.ProgramID
	staged, err := r.screenProgramOutputs(ctx, run, stdout, stderr, scope)
	if err != nil {
		return ProgramOutcome{}, err
	}
	defer staged.close()
	target := evidence.ProgramProvenanceTarget{SessionID: run.tc.Identity.SessionID, ChatSessionID: run.tc.ChatSessionID(), ProjectDir: run.projectDir()}
	for i := range staged {
		if err := r.attachProgramStream(ctx, run, outputs, &staged[i]); err != nil {
			return ProgramOutcome{}, err
		}
	}
	prov, err := r.programHTTPProvenance(ctx, run, staged.hasVisibleOutput())
	if err != nil {
		return ProgramOutcome{}, err
	}
	if err := r.Provenance.StageProgramPublication(ctx, target, prov); err != nil {
		return ProgramOutcome{}, err
	}
	if err := r.Provenance.CommitProgramPublication(ctx, target.SessionID, programID); err != nil {
		return ProgramOutcome{}, err
	}
	if err := r.materializeProgramOutputs(ctx, outputs, target.SessionID, programID, run.tc.Host.SessionOutputDir); err != nil {
		return ProgramOutcome{}, err
	}
	projection, err := r.Recorder.ProjectProgram(ctx, run.tc.Identity.SessionID, run.root.Identity.ProgramID, 4)
	if err != nil {
		return ProgramOutcome{}, err
	}
	if run.tc.Effects.Out != nil {
		receipt, activityErr := r.Recorder.ActivitySummary(ctx, run.tc.Identity.SessionID, run.root.Identity.ProgramID)
		if activityErr != nil {
			receipt = projection.Program
		}
		run.tc.Effects.Out.CommandProgram = &receipt
	}
	out := ProgramOutcome{
		ProgramID:                programID,
		DetailRef:                projection.DetailReference,
		State:                    projection.Program.State,
		Status:                   result.Status,
		StopCode:                 result.StopCode,
		ExecutedChildren:         projection.ExecutedChildren,
		CompletedEffects:         projection.CompletedEffects,
		UncertainEffects:         projection.UncertainEffects,
		NativeGaps:               projection.NativeGaps,
		OmittedNonzeroExecutions: projection.OmittedNonzero,
	}

	for _, child := range projection.Nonzero {
		receipt := child.Receipt
		observed := ProgramExecutionOutcome{EvidenceKind: receipt.EvidenceKind, ExecutionID: receipt.ID, SourceUnitID: child.SourceUnitID, Span: child.Span,
			State: receipt.State, StatusCode: receipt.StatusCode, OwnerStarted: receipt.OwnerStarted, OwnerInvoked: receipt.OwnerInvoked,
			OutputPublishable: receipt.OutputPublishable, DetailRef: receipt.DetailRef}
		for _, output := range receipt.Outputs {
			path, err := (scratch.CommandOutput{ProgramID: programID, ExecutionID: receipt.ID, Stream: output.Stream}).Path()
			if err != nil {
				return ProgramOutcome{}, err
			}
			observed.Outputs = append(observed.Outputs, ProgramExecutionOutput{Stream: output.Stream, Ref: output.Ref, OutputPath: "@output/" + path, SizeBytes: output.SizeBytes, Complete: output.Complete})
		}
		out.NonzeroExecutions = append(out.NonzeroExecutions, observed)
	}
	for _, stream := range staged {
		if err := r.recordProgramStreamDelivery(ctx, run, stream); err != nil {
			return ProgramOutcome{}, err
		}
		window := stream.window
		if stream.name == "stdout" {
			out.Stdout, out.StdoutRef = stream.inline, window.Ref
		} else {
			out.Stderr, out.StderrRef = stream.inline, window.Ref
		}
		out.Outputs = append(out.Outputs, window)
		out.Truncated = out.Truncated || window.Truncated
	}
	if err := r.Provenance.MarkProgramPublicationMaterialized(ctx, target.SessionID, programID); err != nil {
		return ProgramOutcome{}, err
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
		Policy:        secretmatch.StreamPolicy{ManagedEchoes: true},
		WorkingMemory: run.memory,
		Bounds:        secretmatch.StreamBounds{InputBytes: maximum, OutputBytes: maximum, MemoryBytes: 32 << 20, DetectorWork: 1 << 30},
	})
	_ = output.CloseWithError(screenErr)
	result := <-done
	if result.err != nil {
		return nil, false, result.err
	}
	spill, err := retainOutcome(result.spill, screenErr)
	return spill, metadata.Redacted, err
}

// retainOutcome applies the retained stream's publication eligibility: a spill
// that failed for a non-limit reason holds an incomplete prefix and must never
// be delivered as the stream's output, while a limit-bounded prefix keeps its
// visible limit metadata.
func retainOutcome(spill *tooloutput.StreamSpill, screenErr error) (*tooloutput.StreamSpill, error) {
	if spill == nil {
		return nil, screenErr
	}
	if outputDeliveryLimit(spill.Failure) != nil {
		return spill, nil
	}
	if spill.Failure != nil {
		return nil, errors.Join(spill.Failure, screenErr)
	}
	return spill, screenErr
}

type screenedProgramOutput struct {
	stream        *programStream
	spill         *tooloutput.StreamSpill
	name          string
	budget        int
	redacted      bool
	inline        string
	window        api.CommandProgramOutputWindow
	ranges        []deliveredOutputPart
	releaseSample func()
}

type screenedProgramOutputs []screenedProgramOutput

func (outputs screenedProgramOutputs) close() {
	for _, output := range outputs {
		if output.releaseSample != nil {
			output.releaseSample()
		}
		_ = output.spill.Close()
	}
}

func (outputs screenedProgramOutputs) hasVisibleOutput() bool {
	for _, output := range outputs {
		if output.inline != "" {
			return true
		}
	}
	return false
}

func (r *ProgramRuntime) screenProgramOutputs(ctx context.Context, run *programRun, stdout, stderr *programStream, scope captureprojection.Scope) (screenedProgramOutputs, error) {
	stdoutBudget, stderrBudget := splitStreamBudgets(CommandOutputBudget(run.tc.Host.ModelContextWindow), int(stdout.Len()), int(stderr.Len()))
	var outputs screenedProgramOutputs
	for _, output := range []screenedProgramOutput{
		{stream: stdout, name: "stdout", budget: stdoutBudget},
		{stream: stderr, name: "stderr", budget: stderrBudget},
	} {
		if output.stream.Len() == 0 {
			continue
		}
		spill, redacted, err := r.retainProgramStream(ctx, run, output.stream, output.name, scope)
		if err != nil {
			if spill != nil {
				_ = spill.Close()
			}
			outputs.close()
			return nil, err
		}
		output.spill, output.redacted = spill, redacted
		outputs = append(outputs, output)
	}
	return outputs, nil
}

func (r *ProgramRuntime) attachProgramStream(ctx context.Context, run *programRun, outputs *invocation.ProgramOutputRetention, output *screenedProgramOutput) error {
	stream, spill, name, redacted := output.stream, output.spill, output.name, output.redacted
	program := run.root.Identity.ProgramID
	reference := "output:program:" + program + ":" + name
	outputPath := "@output/" + program + "." + name
	body, err := spill.Open(ctx)
	if err != nil {
		return err
	}
	releaseSample, err := run.memory.Reserve(ctx, int64(2*output.budget+64<<10))
	if err != nil {
		_ = body.Close()
		return err
	}
	output.releaseSample = releaseSample
	sample, err := sampleOutput(body, output.budget)
	if err := errors.Join(err, body.Close()); err != nil {
		return err
	}
	complete := !stream.truncated && spill.Complete
	output.inline, output.window, output.ranges, err = sample.window(ctx, name, reference, outputPath, complete)
	if err != nil {
		return err
	}
	output.window.Limit = outputDeliveryLimit(spill.Failure)
	var mappings []toolcontract.StreamSourceMapping
	if !redacted && complete {
		snapshot, err := stream.snapshotMappings(ctx, spill.Blob.Size)
		if err == nil && snapshot != nil {
			resolved := r.resolveOutputMappings(ctx, run, snapshot.Mappings, spill.Blob.Size)
			_ = snapshot.Close()
			defer resolved.Close()
			mappings = resolved.values
		}
	}
	retained := invocation.RetainedOutput{Stream: name, Reference: reference, Complete: complete, Redacted: redacted, Mappings: mappings}
	if _, err := outputs.Attach(ctx, program, retained, spill); err != nil {
		return err
	}
	return outputs.AttachDeliveryWindow(ctx, program, output.window)
}

func (r *ProgramRuntime) recordProgramStreamDelivery(ctx context.Context, run *programRun, output screenedProgramOutput) error {
	program := run.root.Identity.ProgramID
	session := run.tc.Identity.SessionID
	window := output.window
	resolved, err := r.Recorder.ReadProgramOutputMappings(ctx, session, program, window.Ref, run.memory)
	if err != nil {
		return err
	}
	defer resolved.Close()
	for _, part := range output.ranges {
		r.commitStreamSourceDeliveries(ctx, run, program, window.Ref, part.data, resolved.Mappings, [][2]int64{{part.start, part.start + int64(len(part.data))}})
	}
	if window.Stream == "stdout" {
		r.commitProgramWindows(ctx, run, program, window.Ref, output.ranges)
	}
	return nil
}
