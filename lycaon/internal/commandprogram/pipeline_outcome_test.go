package commandprogram

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func stageOutcome(id string, status int) PipelineStageOutcome {
	return PipelineStageOutcome{ExecutionID: id, Result: completedOutcome(status), RequiredInputComplete: true}
}

func TestPipelineStatusCompletenessAndPublicationAreIndependent(t *testing.T) {
	for _, comparison := range []bool{false, true} {
		first := stageOutcome("producer", 1)
		if comparison {
			first.Result.PublicationEligible = true
			first.Result.PublicationBasis = toolcontract.NativeComparisonPublication
		}
		stages := []PipelineStageOutcome{first, stageOutcome("consumer", 0)}
		latch := &OutcomeLatch{}
		out, err := SettlePipeline("pipeline", stages, latch)
		testutil.FailErr(t, "settle comparison/external pipeline", err)
		if out.Status != 1 || !out.Complete || out.PublicationEligible != comparison || latch.AdmitNext() != nil {
			t.Fatalf("comparison=%t outcome=%+v", comparison, out)
		}
	}
	stages := []PipelineStageOutcome{stageOutcome("early", 3), stageOutcome("middle", 7), stageOutcome("final", 0)}
	out, err := SettlePipeline("pipeline", stages, &OutcomeLatch{})
	testutil.FailErr(t, "retain all statuses", err)
	if out.Status != 7 || out.PublicationEligible {
		t.Fatal("final success hid earlier producer failure")
	}
}

func TestExpectedEarlyClosureNormalizesOnlyItsOwnProducer(t *testing.T) {
	pipe := fixturePipe(t, t.Context(), 100)
	testutil.FailErr(t, "close satisfied prefix consumer", pipe.CloseConsumer())
	_, closeCause := pipe.Write([]byte("after close"))
	producer := stageOutcome("prefix-producer", 141)
	producer.Result.Terminal = toolcontract.ProducerFailed
	producer.Result.Complete = false
	producer.RequiredInputComplete = false
	producer.Result.Evidence = toolcontract.OperationEvidence{Owner: "processes", EvidenceRef: "original-producer-receipt"}
	producer.Closure = NativeConsumerClosure(pipe, closeCause, "prefix-producer", "head")
	stages := []PipelineStageOutcome{producer, stageOutcome("head", 0)}
	out, err := SettlePipeline("pipeline", stages, &OutcomeLatch{})
	testutil.FailErr(t, "settle expected early consumer", err)
	if out.Status != 0 || !out.Complete || !out.PublicationEligible || stages[0].Result.Status != 141 || stages[0].Result.Complete || stages[0].Result.Evidence.EvidenceRef != "original-producer-receipt" {
		t.Fatalf("closure overwrote owner facts or lost normal completion: %+v", out)
	}
	earlier := stageOutcome("genuine-earlier-failure", 2)
	earlier.Result.Terminal = toolcontract.ProducerFailed
	earlier.Result.Complete = false
	stages = append([]PipelineStageOutcome{earlier}, stages...)
	out, err = SettlePipeline("pipeline", stages, &OutcomeLatch{})
	testutil.FailErr(t, "retain earlier utility failure", err)
	if out.Status != 2 || out.Complete || out.PublicationEligible {
		t.Fatal("early consumer concealed another failed producer")
	}
	// A compound error retains its other failure rather than borrowing the close exception.
	producer.Closure = NativeConsumerClosure(pipe, errors.Join(closeCause, errors.New("synthetic independent utility failure")), "prefix-producer", "head")
	stages = []PipelineStageOutcome{producer, stageOutcome("head", 0)}
	latch := &OutcomeLatch{}
	out, err = SettlePipeline("pipeline", stages, latch)
	testutil.FailErr(t, "settle unclassified failure", err)
	if out.Status != 141 || out.Complete || out.PublicationEligible || latch.AdmitNext() == nil {
		t.Fatal("unclassified failure gained expected-close exception")
	}
}

type executorClosureFixture bool

func (f executorClosureFixture) ExpectedConsumerClosure() bool { return bool(f) }

func TestExternalConsumerClosureFollowsExecutorClassification(t *testing.T) {
	for _, classified := range []bool{true, false} {
		producer := stageOutcome("external-producer", 141)
		producer.Result.Terminal = toolcontract.ProducerFailed
		producer.Result.Complete = false
		producer.RequiredInputComplete = false
		producer.Result.EffectCertainty = toolcontract.EffectUnknown
		producer.Closure = ExternalConsumerClosure(executorClosureFixture(classified), "external-producer", "head")
		out, err := SettlePipeline("pipeline", []PipelineStageOutcome{producer, stageOutcome("head", 0)}, &OutcomeLatch{})
		testutil.FailErr(t, "settle external closure", err)
		if classified != (out.Status == 0 && out.Complete && out.PublicationEligible) {
			t.Fatalf("executor classified=%t outcome=%+v", classified, out)
		}
	}
}

func TestPipelineHostRefusalAndBudgetCannotBeMasked(t *testing.T) {
	first := stageOutcome("refused", 1)
	first.Result.Terminal = toolcontract.ProducerRefused
	first.Result.Complete = false
	latch := &OutcomeLatch{}
	out, err := SettlePipeline("pipeline", []PipelineStageOutcome{first, stageOutcome("late-success", 0)}, latch)
	testutil.FailErr(t, "settle denied producer", err)
	if out.PublicationEligible || latch.AdmitNext() == nil {
		t.Fatal("last success concealed host refusal")
	}
	stages := make([]PipelineStageOutcome, 33)
	for i := range stages {
		stages[i] = stageOutcome("fixture-stage", 0)
	}
	latch = &OutcomeLatch{}
	_, err = SettlePipeline("over-limit-pipeline", stages, latch)
	var limit *ProgramLimitError
	if !errors.As(err, &limit) || limit.Limit != "pipeline_stages" || latch.AdmitNext() == nil {
		t.Fatal("pipeline resource refusal was not terminal")
	}
}

func TestNativeConsumerClosureDoesNotRepairUnresolvedOwnerEffect(t *testing.T) {
	for _, certainty := range []toolcontract.EffectCertainty{toolcontract.EffectUnknown, toolcontract.EffectAttempted} {
		pipe := fixturePipe(t, t.Context(), 100)
		testutil.FailErr(t, "close prefix consumer", pipe.CloseConsumer())
		_, closeCause := pipe.Write([]byte("after close"))
		producer := stageOutcome("effectful-producer", 141)
		producer.Result.Terminal = toolcontract.ProducerFailed
		producer.Result.Complete = false
		producer.Result.EffectCertainty = certainty
		producer.RequiredInputComplete = false
		producer.Closure = NativeConsumerClosure(pipe, closeCause, "effectful-producer", "head")
		latch := &OutcomeLatch{}
		out, err := SettlePipeline("pipeline", []PipelineStageOutcome{producer, stageOutcome("head", 0)}, latch)
		testutil.FailErr(t, "settle unresolved native effect", err)
		if out.Complete || out.PublicationEligible || out.Status != 141 || latch.AdmitNext() == nil {
			t.Fatalf("byte closure repaired owner result: %+v", out)
		}
	}
}

func TestNativeConsumerClosureRequiresItsOwnImmediateEndpoint(t *testing.T) {
	first, second := fixturePipe(t, t.Context(), 100), fixturePipe(t, t.Context(), 100)
	testutil.FailErr(t, "close first", first.CloseConsumer())
	testutil.FailErr(t, "close second", second.CloseConsumer())
	_, firstCause := first.Write([]byte("after close"))
	if !errors.Is(firstCause, ErrConsumerClosed) {
		t.Fatalf("diagnostic close lost: %v", firstCause)
	}
	for _, c := range []struct {
		name    string
		pipe    *BytePipe
		cause   error
		allowed bool
	}{
		{"exact endpoint", first, firstCause, true},
		{"wrong endpoint", second, firstCause, false},
		{"diagnostic sentinel", first, ErrConsumerClosed, false},
		{"wrapped endpoint", first, fmt.Errorf("synthetic context: %w", firstCause), false},
		{"joined failure", first, errors.Join(firstCause, errors.New("synthetic utility failure")), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			producer := stageOutcome("producer", 141)
			producer.Result.Terminal, producer.Result.Complete = toolcontract.ProducerFailed, false
			producer.RequiredInputComplete = false
			producer.Closure = NativeConsumerClosure(c.pipe, c.cause, "producer", "consumer")
			out, err := SettlePipeline("pipeline", []PipelineStageOutcome{producer, stageOutcome("consumer", 0)}, &OutcomeLatch{})
			testutil.FailErr(t, "settle endpoint proof", err)
			if out.PublicationEligible != c.allowed || out.Complete != c.allowed || (out.Status == 0) != c.allowed {
				t.Fatalf("allowed=%v outcome=%+v", c.allowed, out)
			}
		})
	}
}
