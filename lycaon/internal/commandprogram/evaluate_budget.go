package commandprogram

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

const limitExceeded = "COMMAND_LIMIT_EXCEEDED"

// Settlement reservation per live execution, from the catalog's
// settlement_reservation rule: a child is admitted only while every started
// execution and the parent keep their final-settlement trace capacity.
const (
	settlementEvents = 32
	settlementBytes  = 16384
	traceRecordBytes = 256
	// A retained diagnostic includes its error reference and at most one
	// stdout endpoint token. It never retains the closed pipe graph.
	traceDiagnosticBytes = 32
)

type evaluationLimits map[string]int64

var evaluationLimitIDs = []string{"statement_visits", "loop_iterations", "function_depth", "source_depth", "child_executions", "concurrent_processes", "trace_events", "trace_bytes", "pipeline_stages", "stream_bytes"}

var catalogEvaluationLimits = sync.OnceValues(func() (evaluationLimits, error) {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return nil, err
	}
	limits := evaluationLimits{}
	for _, limit := range catalog.Language.Limits {
		limits[limit.ID] = limit.Maximum
	}
	for _, id := range evaluationLimitIDs {
		if limits[id] < 1 {
			return nil, fmt.Errorf("compiled language has no %s ceiling", id)
		}
	}
	return limits, nil
})

// evaluation is the program-wide state shared by every frame, pipeline stage
// and background job of one invocation. Counters are cumulative per program.
type evaluation struct {
	ports       EvaluationPorts
	waits       *executionWaits
	expander    *WordExpander
	limits      evaluationLimits
	programName string

	mu           sync.Mutex
	counters     map[string]int64
	live         int64
	traceEvents  int64
	traceBytes   int64
	children     []ChildRecord
	sources      []SourceRecord
	jobs         []string
	procSubstFDs map[int]bool
	// cpuUnmeasured is set when a time report counted native operations.
	cpuUnmeasured atomic.Bool
}

// charge counts one unit of a cumulative program ceiling before the work it
// admits. Exceeding it latches an owner stop and refuses at the reached node.
func (e *evaluation) charge(f *frame, loc commandsurface.SyntaxLocation, limit string, n int64) error {
	e.mu.Lock()
	if e.counters == nil {
		e.counters = map[string]int64{}
	}
	observed := e.counters[limit] + n
	if observed <= e.limits[limit] {
		e.counters[limit] = observed
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()
	return f.refuseLimit(loc, limit, observed)
}

func (e *evaluation) release(limit string, n int64) {
	e.mu.Lock()
	e.counters[limit] -= n
	e.mu.Unlock()
}

// childRun owns one dynamic execution's identity and settlement reservation.
// producer is false for executions (redirections) that never stand in for a
// pipeline stage's producer result.
type childRun struct {
	frame    *frame
	inv      toolcontract.OperationInvocation
	record   ChildRecord
	settled  bool
	producer bool
	// recorded executions settle their receipt through ports.Executions.
	recorded bool
	ctx      context.Context
}

// beginRecorded allocates an execution the evaluator itself runs (pipelines,
// groups, functions, substitutions, jobs, in-process builtins, redirections)
// and records it, so every dispatched descendant has a recorded parent.
func (f *frame) beginRecorded(ctx context.Context, loc commandsurface.SyntaxLocation, command string) (*childRun, error) {
	if f.activeHost != nil && f.activeHost.inv.Identity.NodeID == string(loc.ID) && f.activeHost.inv.SourceUnitID == f.unit {
		child := f.activeHost
		f.activeHost = nil
		return child, nil
	}
	child, err := f.begin(loc, command)
	if err != nil || f.e.ports.Executions == nil {
		return child, err
	}
	if err := f.e.ports.Executions.BeginExecution(ctx, child.inv, command); err != nil {
		child.close(ctx)
		return nil, err
	}
	child.recorded, child.ctx = true, ctx
	return child, nil
}

// begin allocates a dynamic execution for a reached node. Its trace capacity is
// reserved until close records exactly one settlement.
func (f *frame) begin(loc commandsurface.SyntaxLocation, command string) (*childRun, error) {
	e := f.e
	e.mu.Lock()
	events := e.traceEvents + (e.live+2)*settlementEvents
	bytes := e.traceBytes + (e.live+2)*settlementBytes
	if events > e.limits["trace_events"] || bytes > e.limits["trace_bytes"] {
		e.mu.Unlock()
		limit, observed := "trace_events", events
		if events <= e.limits["trace_events"] {
			limit, observed = "trace_bytes", bytes
		}
		return nil, f.refuseLimit(loc, limit, observed)
	}
	e.live++
	e.mu.Unlock()
	inv := NextExecution(f.parent, string(loc.ID), loc.Span)
	return &childRun{frame: f, inv: inv, producer: true, record: ChildRecord{Identity: inv.Identity, SourceUnitID: inv.SourceUnitID, Span: loc.Span, Command: command}}, nil
}

func (c *childRun) settle(ctx context.Context, result toolcontract.ProducerResult, diagnostic error) {
	if c.settled {
		return
	}
	if diagnostic != nil {
		c.frame.e.mu.Lock()
		c.frame.e.traceBytes += traceDiagnosticBytes
		c.frame.e.mu.Unlock()
	}
	c.record.Executed, c.record.Result, c.record.Diagnostic = true, result, diagnostic
	if c.producer {
		record := c.record
		c.frame.state.last = &record
	}
	c.close(ctx)
}

func (c *childRun) close(ctx context.Context) {
	if c == nil || c.settled {
		return
	}
	c.settled = true
	if c.recorded {
		_ = c.frame.e.ports.Executions.SettleExecution(context.WithoutCancel(c.ctx), c.inv, c.record)
	}
	e := c.frame.e
	e.mu.Lock()
	defer e.mu.Unlock()
	e.live--
	e.traceEvents++
	e.traceBytes += traceRecordBytes + int64(len(c.record.Command)+len(c.record.Identity.ExecutionID)+len(c.record.Identity.NodeID)+len(c.record.SourceUnitID))
	e.children = append(e.children, c.record)
}

func (e *evaluation) outcome(root *frame) ProgramOutcome {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := ProgramOutcome{Status: root.state.control.Status(), Exited: root.state.control.Exited(),
		Children: append([]ChildRecord(nil), e.children...), Sources: append([]SourceRecord(nil), e.sources...), Jobs: append([]string(nil), e.jobs...),
		CPUTimeUnmeasured: e.cpuUnmeasured.Load()}
	if stop, ok := root.state.latch.Stop(); ok {
		out.Stop = &stop
	}
	return out
}

// refuseLimit latches the host stop before returning the typed refusal, so no
// later branch, negation or sequence can admit further work.
func (f *frame) refuseLimit(loc commandsurface.SyntaxLocation, limit string, observed int64) error {
	cause := &ProgramLimitError{Limit: limit, Maximum: f.e.limits[limit], Observed: observed}
	return f.refuse(loc, limitExceeded, "bounded "+limit, cause)
}

func (f *frame) refuse(loc commandsurface.SyntaxLocation, code, expected string, cause error) *EvaluationRefusal {
	_ = f.state.latch.Record(uuid.NewString(), toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: code, EffectCertainty: toolcontract.NoEffect}, true)
	return &EvaluationRefusal{Code: code, Node: loc, Expected: expected, Cause: cause}
}

// interrupt latches cancellation observed between statements.
func (f *frame) interrupt(err error) error {
	_ = f.state.latch.Record(uuid.NewString(), toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.NoEffect}, true)
	return err
}

// ownerResult classifies an error returned by a port that produced no
// ProducerResult of its own (staging, input open, source capture, predicates).
// Cancellation interrupts, typed owner rejections and resource ceilings stop
// the program; anything else is an ordinary failure the program may handle.
func ownerResult(err error) toolcontract.ProducerResult {
	var domain toolcontract.ProducerFailure
	if errors.As(err, &domain) {
		return domain.ProducerResult()
	}
	var coded interface{ MachineErrorCode() string }
	var program *ProgramLimitError
	var memory *toolcontract.ProgramMemoryLimitError
	var stream *StreamLimitError
	var predicate *PredicateLimitError
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.NoEffect}
	case errors.As(err, &program) || errors.As(err, &memory) || errors.As(err, &stream) || errors.As(err, &predicate):
		return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, StopCode: limitExceeded, EffectCertainty: toolcontract.NoEffect}
	case errors.As(err, &coded) && coded.MachineErrorCode() != "":
		return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: coded.MachineErrorCode(), EffectCertainty: toolcontract.NoEffect}
	default:
		return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}
	}
}

func ordinaryResult(status int) toolcontract.ProducerResult {
	return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: status == 0, EffectCertainty: toolcontract.NoEffect}
}
