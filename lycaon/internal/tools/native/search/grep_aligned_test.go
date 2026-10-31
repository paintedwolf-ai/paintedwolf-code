package search

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type alignedSpillQuota struct {
	maximum, held, peak int64
	failure             error
}

func (q *alignedSpillQuota) ReserveSpill(_ context.Context, n int64) (func(), error) {
	if q.failure != nil {
		return nil, q.failure
	}
	if n > q.maximum-q.held {
		return nil, errors.New("fixture spill quota exhausted")
	}
	q.held += n
	q.peak = max(q.peak, q.held)
	return func() { q.held -= n }, nil
}
func TestGrepAlignedStdinUsesBoundedSpoolAndReleasesQuota(t *testing.T) {
	for _, input := range []string{"yes\nno\n", "yes\nno\nx", "yes\nno\nxx"} {
		h := newHarness(t, nil)
		quota := &alignedSpillQuota{maximum: 8}
		h.owners.Admission = quota
		h.owners.overrides = map[string]int64{"spill_bytes": 8}
		memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 1 << 20})
		testutil.FailErr(t, "toolcontract.NewProgramMemory failed", err)
		h.owners.Revisions.Memory = memory
		result := h.exec(input, "grep", "-T", "-n", "yes")
		if len(input) > 8 {
			result.refused(t, "aligned spill capacity", "COMMAND_LIMIT_EXCEEDED")
		} else {
			result.expect(t, "aligned prefixes", "1:\tyes\n", "", 0)
		}
		if quota.held != 0 || quota.peak != 8 || memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
			t.Fatalf("held spill %d peak %d memory %+v", quota.held, quota.peak, memory.InUse())
		}
	}
}
func TestGrepAlignedStdinRefusesQuotaBeforeReading(t *testing.T) {
	h := newHarness(t, nil)
	failure := errors.New("quota unavailable")
	h.owners.Admission = &alignedSpillQuota{failure: failure}
	result := h.exec("yes\n", "grep", "-T", "yes")
	if !errors.Is(result.err, failure) || result.stdout != "" {
		t.Fatalf("quota failure escaped %q %v", result.stdout, result.err)
	}
}
