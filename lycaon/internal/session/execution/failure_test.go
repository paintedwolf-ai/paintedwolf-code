package execution

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
)

type sliceTurnError []string

func (sliceTurnError) Error() string { return "slice error" }

func TestTurnFailureReportingPreservesUnreportedCauses(t *testing.T) {
	provider := &failure.ProviderEmptyCompletionError{ProviderID: "fixture"}
	cleanup := errors.New("cleanup failed")
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	var delivered []error
	mgr := &Journal{}
	mgr.SetFailureSink(func(ctx context.Context, id string, err error) {
		if ctx.Err() != nil || id != "session" {
			t.Errorf("delivery context/id = %v/%q", ctx.Err(), id)
		}
		delivered = append(delivered, err)
	})
	reported := mgr.ReportFailure(canceled, "session", provider)
	if len(delivered) != 1 || !errors.Is(delivered[0], provider) || !errors.Is(reported, provider) {
		t.Fatalf("reported failure lost its cause: delivered=%v returned=%v", delivered, reported)
	}
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"nil", nil, nil},
		{"reported", reported, nil},
		{"wrapped report", fmt.Errorf("submission: %w", reported), nil},
		{"joined reports", errors.Join(reported, reported), nil},
		{"unreported", cleanup, cleanup},
		{"joined cleanup", errors.Join(reported, cleanup), cleanup},
		{"wrapped joined cleanup", fmt.Errorf("drain: %w", errors.Join(reported, cleanup)), cleanup},
		{"nested cleanup", errors.Join(reported, fmt.Errorf("persist: %w", cleanup)), cleanup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UnreportedTurnFailure(tc.err)
			if !errors.Is(got, tc.want) {
				t.Fatalf("remaining error = %v, want %v", got, tc.want)
			}
			if got != nil && errors.Is(got, provider) {
				t.Fatal("reported provider failure would be published again")
			}
		})
	}
	if got := UnreportedTurnFailure(fmt.Errorf("uncomparable: %w", sliceTurnError{"failure"})); got == nil {
		t.Fatal("uncomparable error was discarded")
	}
	if got := (&Journal{}).ReportFailure(t.Context(), "session", provider); UnreportedTurnFailure(got) == nil {
		t.Fatal("missing sink marked failure as delivered")
	}
}
