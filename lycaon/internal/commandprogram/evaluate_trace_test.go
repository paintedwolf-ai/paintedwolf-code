package commandprogram

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestRetainedEndpointDiagnosticUsesTraceBudget(t *testing.T) {
	limits, err := catalogEvaluationLimits()
	if err != nil {
		testutil.FailErr(t, "prepare invocation boundary", err)
	}
	local := evaluationLimits{}
	for id, value := range limits {
		local[id] = value
	}
	parent := fixtureInvocation()
	loc := commandsurface.SyntaxLocation{ID: "producer", Span: parent.Span}
	recordBytes := int64(traceRecordBytes + len("producer") + 36 + len(loc.ID) + len(parent.SourceUnitID))
	local["trace_bytes"] = 2*settlementBytes + recordBytes
	e := &evaluation{limits: local}
	f := &frame{e: e, parent: parent, state: &frameState{latch: &OutcomeLatch{}}}
	budget, err := NewByteBudget("stream_throughput_bytes", 1024)
	if err != nil {
		testutil.FailErr(t, "prepare invocation boundary", err)
	}
	pipe, err := NewBytePipe(t.Context(), budget)
	if err != nil {
		testutil.FailErr(t, "prepare invocation boundary", err)
	}
	if err := pipe.CloseConsumer(); err != nil {
		testutil.FailErr(t, "prepare invocation boundary", err)
	}
	_, cause := pipe.Write([]byte("unconsumed"))
	child, err := f.begin(loc, "producer")
	if err != nil {
		testutil.FailErr(t, "prepare invocation boundary", err)
	}
	child.settle(t.Context(), toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, cause)
	if e.traceBytes != recordBytes+traceDiagnosticBytes || len(e.children) != 1 || !NativeConsumerClosure(pipe, e.children[0].Diagnostic, "producer", "consumer").recognized || e.live != 0 {
		t.Fatalf("retained trace bytes=%d records=%+v live=%d", e.traceBytes, e.children, e.live)
	}
	_, err = f.begin(loc, "producer")
	assertLimit(t, err, "trace_bytes", local["trace_bytes"], local["trace_bytes"]+traceDiagnosticBytes)
	if len(e.children) != 1 || e.live != 0 {
		t.Fatal("trace refusal admitted another execution")
	}
	if _, stopped := f.state.latch.Stop(); !stopped {
		t.Fatal("trace refusal did not stop continuation")
	}
}

func TestIsolatePrefixAssignmentFailureReleasesClone(t *testing.T) {
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1, State: variableBytes})
	if err != nil {
		testutil.FailErr(t, "prepare invocation boundary", err)
	}
	vars, err := NewVariables(t.Context(), memory, "program", nil, nil)
	if err != nil {
		testutil.FailErr(t, "prepare invocation boundary", err)
	}
	defer vars.Close()
	log := &dispatchLog{}
	f := &frame{state: &frameState{vars: vars, latch: &OutcomeLatch{}, dispatch: &fakeDispatch{log: log}}, prefix: map[string]string{"A": "v"}}
	child, release, err := f.isolate(t.Context(), nil)
	var limit *toolcontract.ProgramMemoryLimitError
	if !errors.As(err, &limit) || limit.Account != "state" || limit.Maximum != variableBytes || limit.Observed != variableBytes+2 || child != nil || release != nil {
		t.Fatalf("child=%v release=%v error=%v", child, release != nil, err)
	}
	if log.forks != 0 || memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("forks=%d memory=%+v", log.forks, memory.InUse())
	}
}
