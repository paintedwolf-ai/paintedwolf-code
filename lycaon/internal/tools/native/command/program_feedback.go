package command

import (
	"context"
	"errors"
	"io"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcommand"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// feedback renders the first native owner rejection through the program
// feedback boundary, with the program's completed and uncertain effects.
func (r *ProgramRuntime) feedback(ctx context.Context, run *programRun, dispatcher *ProgramDispatcher, stderr io.Writer, evalErr error) (refusal *ProgramRefusal, release func(), err error) {
	defer func() {
		if release == nil {
			release = func() {}
		}
	}()
	rejection := dispatcher.firstRejection()

	if rejection == nil {
		var rejection *toolrejection.ToolReject
		var evaluationRefusal *commandprogram.EvaluationRefusal
		if errors.As(evalErr, &evaluationRefusal) {
			span := evaluationRefusal.Node.Span
			data := map[string]any{"tool": "command", "source_start_byte": span.StartByte, "source_end_byte": span.EndByte, "source_line": span.Line, "source_column": span.Column, "expected": evaluationRefusal.Expected, "expected_form": evaluationRefusal.Expected, "reason": evaluationRefusal.Expected}
			data["diagnostic_stage"] = string(toolcontract.EvaluationDiagnosticStage)
			if evaluationRefusal.SourceUnitID != "" {
				data["source_unit_id"] = evaluationRefusal.SourceUnitID
			}
			var expansion *commandprogram.ExpansionError
			if errors.As(evaluationRefusal.Cause, &expansion) {
				data["diagnostic_stage"] = string(toolcontract.ExpansionDiagnosticStage)
			}
			if expansion != nil && expansion.Limit != "" {
				data["limit_id"], data["maximum"], data["observed"] = expansion.Limit, expansion.Maximum, expansion.ObservedValue()
			}
			if evaluationRefusal.Command != "" {
				data["command"], data["word"], data["expected_form"] = evaluationRefusal.Command, evaluationRefusal.Option, evaluationRefusal.Form
				if evaluationRefusal.Code == "COMMAND_OPTION_UNSUPPORTED" {
					data["diagnostic_stage"] = string(toolcontract.OptionDiagnosticStage)
				}
			}
			if syntax := toolcommand.ProgramSyntaxObservation("command", evaluationRefusal.Cause); syntax != nil {
				for key, value := range syntax.Data {
					data[key] = value
				}
				if evaluationRefusal.SourceUnitID != "" {
					data["operation_source_unit_id"] = evaluationRefusal.SourceUnitID
				}
				data["operation_source_start_byte"], data["operation_source_end_byte"] = span.StartByte, span.EndByte
				data["operation_source_line"], data["operation_source_column"] = span.Line, span.Column
			}
			rejection = toolrejection.RejectInvalidArguments(evaluationRefusal.Code, data)
			original := toolrejection.AsToolReject(tools.CommandResourceRejection(evaluationRefusal.Cause))
			if original != nil && original.Code == evaluationRefusal.Code {
				copy := *original
				copy.Data = data
				for key, value := range original.Data {
					copy.Data[key] = value
				}
				rejection = &copy
			}
			enrichProgramReject(rejection, evaluationRefusal.Command, span)
			if rejection.Code == "COMMAND_LIMIT_EXCEEDED" && dispatcher.claimStopLine() {
				writeLimitRefusal(stderr, "command", "", rejection)
			}
		} else if rejection = toolrejection.AsToolReject(evalErr); rejection != nil {
			enrichProgramReject(rejection, "", run.root.Span)
		} else {
			return nil, nil, evalErr
		}
		delivery, err := run.executor.Rejections.RenderProgramRefusal(ctx, run.frame, run.memory, run.capture, run.tc, errors.Join(rejection, evalErr), nil, run.recorder)
		if err != nil {
			return nil, nil, errors.Join(evalErr, err)
		}
		refusal = &ProgramRefusal{Code: delivery.Code, Details: delivery.Details, DeliveryFailure: delivery.Failure}
		if delivery.Feedback != nil {
			refusal.Feedback = delivery.Feedback.Error()
		}
		return refusal, delivery.Close, nil
	}

	reject := toolrejection.AsToolReject(rejection.cause)
	enrichProgramReject(reject, rejection.operation.Command(), rejection.operation.Invocation().Span)
	failedDelivery := func(cause error) (*ProgramRefusal, func(), error) {
		var capacity *toolcontract.ProgramMemoryLimitError
		if !errors.As(cause, &capacity) && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
			return nil, nil, errors.Join(rejection.cause, cause)
		}
		delivery, err := run.executor.Rejections.RenderProgramRefusal(ctx, run.frame, run.memory, run.capture, run.tc, rejection.cause, cause, run.recorder)
		if err != nil {
			return nil, nil, errors.Join(rejection.cause, err)
		}
		return &ProgramRefusal{Code: delivery.Code, DeliveryFailure: delivery.Failure}, delivery.Close, nil
	}
	prepared, err := toolexecution.PrepareProgramFailure(ctx, run.frame, run.recorder, run.memory, run.capture, run.tc, rejection.operation, rejection.cause)
	if err != nil {
		return failedDelivery(err)
	}
	if prepared == nil {
		return nil, nil, rejection.cause
	}
	release = prepared.Close
	rendered := run.executor.Rejections.RenderProgramFailure(ctx, run.frame, run.tc, prepared)
	if rendered != nil && toolrejection.AsToolReject(rendered) == nil {
		prepared.Close()
		return failedDelivery(rendered)
	}
	if rendered == nil {
		rendered = toolrejection.RenderReject(prepared.Rejection, nil)
	}
	return &ProgramRefusal{Code: reject.Code, Feedback: rendered.Error(), Details: prepared.Rejection.Data}, release, nil
}
