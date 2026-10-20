package commandprogram

import (
	"fmt"
	"sync"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ConsumerClosure is a host-classified pipe event, not a numeric-status guess.
// The original producer result remains available to the receipt/recovery owner.
type ConsumerClosure struct {
	producer, consumer string
	recognized         bool
	external           bool
}

// ExternalConsumerClosure carries the existing executor's signal classification.
func ExternalConsumerClosure(stage lyexec.StageRun, producerID, consumerID string) ConsumerClosure {
	return ConsumerClosure{producer: producerID, consumer: consumerID, recognized: stage.ExpectedConsumerClosure(), external: true}
}

// NativeConsumerClosure recognizes only the canonical close returned by this
// exact byte endpoint. Other/joined utility errors cannot gain this exception.
func NativeConsumerClosure(pipe *BytePipe, cause error, producerID, consumerID string) ConsumerClosure {
	return ConsumerClosure{producer: producerID, consumer: consumerID, recognized: pipe != nil && cause == ErrConsumerClosed && pipe.Completion().Termination == StreamConsumerClosed}
}

// PipelineStageOutcome is original producer settlement plus input/pipe facts.
// RequiredInputComplete is supplied by the operation's declared input contract.
type PipelineStageOutcome struct {
	ExecutionID           string
	Result                toolcontract.ProducerResult
	RequiredInputComplete bool
	Closure               ConsumerClosure
}

// PipelineOutcome aggregates byte-production facts independently from an
// operation's verification verdict, effect settlement or destination publication.
type PipelineOutcome struct {
	Status                        int
	Complete, PublicationEligible bool
}

var pipelineStageMaximum = sync.OnceValues(func() (int, error) {
	catalog, err := toolcontract.LanguageV1()
	if err != nil {
		return 0, err
	}
	for _, limit := range catalog.Language.Limits {
		if limit.ID == "pipeline_stages" {
			return int(limit.Maximum), nil
		}
	}
	return 0, fmt.Errorf("compiled language has no pipeline stage ceiling")
})

// SettlePipeline keeps every producer in the eligibility decision. A successful
// last stage cannot hide earlier failure, missing required input or host stop.
// The caller retains the stage records under its existing receipt/state lease.
func SettlePipeline(executionID string, stages []PipelineStageOutcome, latch *OutcomeLatch) (PipelineOutcome, error) {
	out := PipelineOutcome{Complete: true, PublicationEligible: true}
	if latch == nil {
		return PipelineOutcome{}, fmt.Errorf("pipeline settlement requires a host-stop latch")
	}
	invalid := toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.EffectUnknown}
	if executionID == "" || len(stages) == 0 {
		invalid.Terminal = ""
		_ = latch.Record(executionID, invalid, true)
		return PipelineOutcome{}, fmt.Errorf("pipeline settlement requires dynamic identity and stages")
	}
	maximum, err := pipelineStageMaximum()
	if err != nil {
		invalid.Terminal = ""
		_ = latch.Record(executionID, invalid, true)
		return PipelineOutcome{}, err
	}
	if len(stages) > maximum {
		invalid.StopCode = "COMMAND_LIMIT_EXCEEDED"
		_ = latch.Record(executionID, invalid, true)
		return PipelineOutcome{}, &ProgramLimitError{Limit: "pipeline_stages", Maximum: int64(maximum), Observed: int64(len(stages))}
	}
	var firstError error
	for i, stage := range stages {
		result := stage.Result
		closure := stage.Closure
		expected := closure.recognized && closure.producer == stage.ExecutionID && i+1 < len(stages) && closure.consumer == stages[i+1].ExecutionID && closure.producer != "" && closure.consumer != ""
		// Authority, interruption and explicit owner stops are never pipe closure.
		// A consumer must have an ordinary settled result. Its utility error and
		// publication eligibility remain independent later factors; normalizing the
		// producer's pipe closure cannot erase the consumer's own failed outcome.
		// A native byte close cannot settle an unresolved mutation. External
		// process effects retain their existing uncertainty independently of the
		// executor's sealed expected-SIGPIPE classification.
		if expected && !closure.external && (result.EffectCertainty == toolcontract.EffectAttempted || result.EffectCertainty == toolcontract.EffectUnknown) {
			expected = false
		}
		if expected {
			consumer := stages[i+1].Result
			expected = result.StopCode == "" && (result.Terminal == toolcontract.ProducerFailed || result.Terminal == toolcontract.ProducerCompleted) && consumer.Validate() == nil && (consumer.Terminal == toolcontract.ProducerCompleted || consumer.Terminal == toolcontract.ProducerFailed) && consumer.StopCode == "" && stages[i+1].RequiredInputComplete
		}
		// Duplicate or missing dynamic IDs are invalid settlement, independently of
		// coincident static nodes or equal ordinary statuses.
		identityValid := stage.ExecutionID != ""
		for j := 0; j < i; j++ {
			if stages[j].ExecutionID == stage.ExecutionID {
				identityValid = false
			}
		}
		if !identityValid {
			result.Terminal = ""
			expected = false
		}
		if err := result.Validate(); err != nil {
			if firstError == nil {
				firstError = err
			}
			expected = false
		}
		// Normalize only the control/byte-plane exception. Never rewrite the original
		// owner result or its effect/recovery references in the supplied stage record.
		effective := result
		inputComplete := stage.RequiredInputComplete
		if expected {
			effective.Status = 0
			effective.Terminal = toolcontract.ProducerCompleted
			effective.Complete = true
			effective.PublicationEligible = true
			effective.PublicationBasis = toolcontract.ZeroStatusPublication
			inputComplete = true
		}
		if err := latch.Record(stage.ExecutionID, effective, inputComplete); err != nil && firstError == nil {
			firstError = err
		}
		if effective.Status != 0 {
			out.Status = effective.Status
		}
		out.Complete = out.Complete && effective.Complete && inputComplete
		out.PublicationEligible = out.PublicationEligible && effective.PublicationEligible
	}
	if _, stopped := latch.Stop(); stopped {
		out.Complete = false
		out.PublicationEligible = false
	}
	out.PublicationEligible = out.PublicationEligible && out.Complete && firstError == nil
	return out, firstError
}
