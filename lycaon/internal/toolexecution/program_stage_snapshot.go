package toolexecution

import (
	"context"
	"fmt"
	"math"
	"sync"
	"unsafe"

	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ProgramStateAdmission is the existing host aggregate ledger's structural port.
// It refuses before allocation and must not wait on this program's own memory.
type ProgramStateAdmission interface {
	ReserveProgramState(context.Context, int64) (func(), error)
}

// NativeStageSnapshot retains one admitted review/detection projection. Keep it
// until policy, held review, dispatch and every derived stage event are released.
// It is not a durable job and carries no effect permit.
type NativeStageSnapshot struct {
	mu        sync.Mutex
	stages    []toolcontract.ExecutionStage
	release   func()
	projected bool
	closed    bool
}

// PrepareNativeStages reserves the known retained projection bound before any
// detached argv allocation. Six copies cover capture, policy, approval action,
// detection observation, event argv and wrapper-normalization scratch. Event
// structs and bounded image/API projections are included; no display reparsing
// or JSON conversion is needed for an already bound stage.
func PrepareNativeStages(ctx context.Context, admission ProgramStateAdmission, stages []toolcontract.ExecutionStage) (*NativeStageSnapshot, error) {
	if ctx == nil || admission == nil || len(stages) == 0 {
		return nil, fmt.Errorf("native stage snapshot requires context, state admission and stages")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bound, err := nativeStageMemory(stages)
	if err != nil {
		return nil, err
	}
	release, err := admission.ReserveProgramState(ctx, bound)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("state admission returned no retained lease")
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return &NativeStageSnapshot{stages: toolcontract.CloneExecutionStages(stages), release: release}, nil
}

func nativeStageMemory(stages []toolcontract.ExecutionStage) (int64, error) {
	const copies = int64(6)
	var total int64
	add := func(value int64) bool {
		if value < 0 || value > math.MaxInt64-total {
			return false
		}
		total += value
		return true
	}
	for _, stage := range stages {
		if stage.ExecutionID == "" || stage.OperationID == "" || len(stage.Argv) == 0 || stage.Argv[0] == "" {
			return 0, fmt.Errorf("native stage requires exact child, operation and argv")
		}
		header := int64(unsafe.Sizeof(stage)) + int64(unsafe.Sizeof(detectionpack.Event{})) + 128
		if !add(header * copies) {
			return 0, fmt.Errorf("native stage memory bound overflow")
		}
		for _, value := range []string{stage.ExecutionID, stage.OperationID, stage.CommandLine, stage.PipelineCommandLine} {
			if int64(len(value)) > math.MaxInt64/copies || !add(int64(len(value))*copies) {
				return 0, fmt.Errorf("native stage string bound overflow")
			}
		}
		for _, argument := range stage.Argv {
			size := int64(len(argument)) + int64(unsafe.Sizeof(argument)) + 64
			if size > math.MaxInt64/copies || !add(size*copies) {
				return 0, fmt.Errorf("native argv memory bound overflow")
			}
		}
	}
	return total, nil
}

// PolicyContext publishes a detached projection once. Rechecking held effects
// uses that exact context and current domain authority; a new binding requires
// a new reservation instead of silently allocating another retained snapshot.
func (s *NativeStageSnapshot) PolicyContext(base platform.PolicyContext) (platform.PolicyContext, error) {
	if s == nil {
		return platform.PolicyContext{}, fmt.Errorf("native stage snapshot required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.projected {
		return platform.PolicyContext{}, fmt.Errorf("native stage snapshot is closed or already projected")
	}
	s.projected = true
	base.BoundStages = toolcontract.CloneExecutionStages(s.stages)
	return base, nil
}

func (s *NativeStageSnapshot) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.stages = nil
	s.release()
}
