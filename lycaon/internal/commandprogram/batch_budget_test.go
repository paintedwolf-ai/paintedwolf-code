package commandprogram

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestBatchBudgetSharesRemainingStateAndPreservesCatalogBounds(t *testing.T) {
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 64 << 20, State: 64 << 20})
	if err != nil {
		t.Fatalf("memory: %v", err)
	}
	held, err := memory.ReserveProgramState(t.Context(), 8<<20)
	if err != nil {
		t.Fatalf("reserve producer: %v", err)
	}
	defer held()
	serial := NewBatchBudget(memory, "/project", 1<<20, 16384, 1)
	parallel := NewBatchBudget(memory, "/project", 1<<20, 16384, 4)
	if !serial.Fits(4096, 100) || !parallel.Fits(4096, 100) || parallel.Fields >= serial.Fields {
		t.Fatalf("batch capacity: serial %+v parallel %+v", serial, parallel)
	}
	if parallel.Fits(1<<20, 16384) {
		t.Fatal("parallel batch ignored the shared state budget")
	}
	var limit *ProgramLimitError
	if !errors.As(parallel.Refusal(1<<20, 16384), &limit) || limit.Limit != "program_state_bytes" || limit.Observed <= limit.Maximum {
		t.Fatalf("resource refusal: %+v", limit)
	}
	if !errors.As(parallel.Refusal((1<<20)+1, 1), &limit) || limit.Limit != "argv_bytes" {
		t.Fatalf("argv refusal: %+v", limit)
	}
	if !errors.As(parallel.Refusal(1, 16385), &limit) || limit.Limit != "expanded_fields" {
		t.Fatalf("field refusal: %+v", limit)
	}
}
