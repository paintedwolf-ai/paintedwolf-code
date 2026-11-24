package command

import (
	"context"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/pkg/api"
)

type executionTransportRecorderKey struct{}

type executionTransportRecorder func(context.Context, confine.GitTransportObservation) error

func withExecutionTransportRecorder(ctx context.Context, fn executionTransportRecorder) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, executionTransportRecorderKey{}, fn)
}

func recordExecutionTransport(ctx context.Context, obs confine.GitTransportObservation) {
	if obs.ExecutionID == "" || obs.Cause == "" {
		return
	}
	fn, _ := ctx.Value(executionTransportRecorderKey{}).(executionTransportRecorder)
	if fn == nil {
		return
	}
	_ = fn(ctx, obs)
}

func toWireExecutionTransport(obs confine.GitTransportObservation) api.CommandExecutionTransport {
	out := api.CommandExecutionTransport{
		Cause:        string(obs.Cause),
		Stage:        obs.Stage,
		Destination:  obs.Destination,
		Host:         obs.Host,
		Port:         int(obs.Port),
		PolicyReason: obs.PolicyReason,
		ErrorClass:   obs.ErrorClass,
	}
	if len(obs.Attempts) > 0 {
		out.Attempts = make([]api.CommandExecutionTransportAttempt, len(obs.Attempts))
		for i, a := range obs.Attempts {
			out.Attempts[i] = api.CommandExecutionTransportAttempt{
				Ordinal:      a.Ordinal,
				Destination:  a.Destination,
				Host:         a.Host,
				Port:         int(a.Port),
				Stage:        a.Stage,
				Outcome:      a.Outcome,
				ReplyCode:    int(a.ReplyCode),
				PolicyReason: a.PolicyReason,
				ErrorClass:   a.ErrorClass,
			}
		}
	}
	return out
}

func projectNonzeroExecution(programID string, child invocation.ExecutionProjection) (ProgramExecutionOutcome, error) {
	receipt := child.Receipt
	observed := ProgramExecutionOutcome{
		EvidenceKind:      receipt.EvidenceKind,
		ExecutionID:       receipt.ID,
		SourceUnitID:      child.SourceUnitID,
		Span:              child.Span,
		State:             receipt.State,
		StatusCode:        receipt.StatusCode,
		EffectCertainty:   receipt.EffectCertainty,
		OwnerStarted:      receipt.OwnerStarted,
		OwnerInvoked:      receipt.OwnerInvoked,
		OutputPublishable: receipt.OutputPublishable,
		DetailRef:         receipt.DetailRef,
		Transport:         receipt.Transport,
	}
	for _, output := range receipt.Outputs {
		path, err := (scratch.CommandOutput{ProgramID: programID, ExecutionID: receipt.ID, Stream: output.Stream}).Path()
		if err != nil {
			return ProgramExecutionOutcome{}, err
		}
		observed.Outputs = append(observed.Outputs, ProgramExecutionOutput{
			Stream: output.Stream, Ref: output.Ref, OutputPath: "@output/" + path, SizeBytes: output.SizeBytes, Complete: output.Complete,
		})
	}
	return observed, nil
}
