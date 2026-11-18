package sweeps

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testbackup"
	"github.com/lycaon/lycaon/test/wiring"
)

// runTerminal runs each terminal operation as the first request of a host
// of its own; its answer counts toward the same findings, corpus and
// coverage as the main sweep's.
func (s *conformanceSweep) runTerminal(t *testing.T) {
	for _, op := range s.ops {
		if stageOf(op) != stageTerminal {
			continue
		}
		sub := s.onHost(wiring.BuildForTest(t))
		sub.notices = conformanceNotices(t)
		req, err := sub.successRequest(t.Context(), op)
		if err != nil {
			s.findings.add(ruleOperationsSucceed, op.ID, "success", "could not prepare: %v", err)
			continue
		}
		sub.send(t.Context(), req)
	}
}

// onHost is a sweep of h that shares this sweep's records but none of its
// fixtures.
func (s *conformanceSweep) onHost(h *wiring.Harness) *conformanceSweep {
	sub := *s
	sub.handler, sub.host = h.Server, h
	sub.values, sub.readObserved = newConformanceValues(), map[string]bool{}
	sub.effects, sub.keyRows, sub.beforeDeletes = nil, nil, nil
	sub.memo = &sweepMemo{}
	return &sub
}

// terminalRecoverySnapshot captures the upgrade-recovery snapshot startup
// writes before an upgrade, which a host offers for restore.
func terminalRecoverySnapshot(_ context.Context, s *conformanceSweep, _ *conformanceOp) (successInputs, error) {
	if s.host == nil || s.fixture.t == nil {
		return successInputs{}, errors.New("no host to capture a recovery snapshot from")
	}
	testbackup.Recovery(s.fixture.t, s.host.DB, s.host.DatabasePath())
	return successInputs{}, nil
}
