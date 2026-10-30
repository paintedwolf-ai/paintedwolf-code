package toolcontract

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProgramMemoryRefusesWithoutWaitingAndReleasesOnce(t *testing.T) {
	memory, err := NewProgramMemory(ProgramMemoryLimits{Working: 100, State: 10})
	if err != nil {
		testutil.FailErr(t, "create bounded program memory", err)
	}
	first, err := memory.Reserve(t.Context(), 60)
	if err != nil {
		testutil.FailErr(t, "reserve working memory", err)
	}
	state, err := memory.ReserveProgramState(t.Context(), 10)
	if err != nil {
		testutil.FailErr(t, "reserve persistent program state", err)
	}
	var limit *ProgramMemoryLimitError
	if _, err := memory.Reserve(t.Context(), 41); !errors.As(err, &limit) || limit.Account != "working" || limit.Maximum != 100 || limit.Observed != 101 || limit.Error() == "" {
		t.Fatalf("working overflow %v", err)
	}
	if _, err := memory.ReserveProgramState(t.Context(), 1); !errors.As(err, &limit) || limit.Account != "state" {
		t.Fatalf("state overflow %v", err)
	}
	if got := memory.InUse(); got != (ProgramMemoryUsage{Working: 60, State: 10}) || memory.AvailableWorking() != 40 {
		t.Fatalf("in use %+v", got)
	}
	first()
	first()
	state()
	if got := memory.InUse(); got != (ProgramMemoryUsage{}) || memory.AvailableWorking() != 100 {
		t.Fatalf("double release changed accounting %+v", got)
	}
	if got := memory.Peak(); got != (ProgramMemoryUsage{Working: 60, State: 10}) || memory.Reservations() != 2 {
		t.Fatalf("peak %+v", got)
	}
	again, err := memory.Reserve(t.Context(), 100)
	if err != nil {
		t.Fatalf("released capacity not reusable: %v", err)
	}
	if memory.AvailableWorking() != 0 {
		t.Fatalf("full account available capacity = %d", memory.AvailableWorking())
	}
	again()
}

func TestProgramMemoryRejectsInvalidRequests(t *testing.T) {
	if _, err := NewProgramMemory(ProgramMemoryLimits{Working: 1}); err == nil {
		t.Fatal("missing state account admitted")
	}
	memory, err := NewProgramMemory(ProgramMemoryLimits{Working: 1, State: 1})
	if err != nil {
		testutil.FailErr(t, "create minimal program memory", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := memory.Reserve(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reservation %v", err)
	}
	if _, err := memory.Reserve(t.Context(), -1); err == nil {
		t.Fatal("negative reservation admitted")
	}
	var missing *ProgramMemory
	if _, err := missing.ReserveProgramState(t.Context(), 1); err == nil {
		t.Fatal("nil ledger admitted")
	}
}

func TestProgramMemoryConcurrentReservationsNeverExceedCapacity(t *testing.T) {
	memory, err := NewProgramMemory(ProgramMemoryLimits{Working: 64, State: 1})
	if err != nil {
		testutil.FailErr(t, "create concurrent reservation ledger", err)
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				if release, err := memory.Reserve(t.Context(), 8); err == nil {
					release()
				}
			}
		}()
	}
	wg.Wait()
	if memory.Peak().Working > 64 || memory.InUse().Working != 0 {
		t.Fatalf("peak %+v in use %+v", memory.Peak(), memory.InUse())
	}
}

func TestCatalogProgramMemoryLimitsUseFrozenCatalog(t *testing.T) {
	limits, err := CatalogProgramMemoryLimits()
	if err != nil {
		testutil.FailErr(t, "load frozen catalog memory limits", err)
	}
	catalog, err := LanguageV1()
	if err != nil {
		testutil.FailErr(t, "load frozen language catalog", err)
	}
	want := ProgramMemoryLimits{}
	for _, limit := range catalog.Language.Limits {
		switch limit.ID {
		case "evaluation_memory_bytes":
			want.Working = limit.Maximum
		case "program_state_bytes":
			want.State = limit.Maximum
		}
	}
	if limits != want || want.Working == 0 || want.State == 0 {
		t.Fatalf("limits %+v want %+v", limits, want)
	}
}
