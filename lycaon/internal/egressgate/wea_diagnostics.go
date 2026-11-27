package egressgate

import (
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

var egressWEADiagSeq atomic.Int64

// observeEgressDecisionWEA emits a bounded WEA egress observation for an
// attributed egress gate decision. Uses a monotonic sequence number so
// legitimate revalidation and later admissions superseding earlier refusals
// are cleanly ordered and never emit false-positive warnings.
// When diagnostics are disabled, this returns immediately with zero allocations.
func observeEgressDecisionWEA(cmd confine.EgressCommand, destination string, allowed bool) bool {
	if !cmddiag.Enabled() {
		return false
	}
	sess := strings.TrimSpace(cmd.SessionID)
	dest := strings.TrimSpace(destination)
	if sess == "" || dest == "" {
		return false
	}
	inv := strings.TrimSpace(cmd.ActionID)
	if inv == "" {
		inv = strings.TrimSpace(cmd.ToolCallID)
	}
	if inv == "" {
		inv = sess
	}
	seq := egressWEADiagSeq.Add(1)
	destDigest := cmddiag.DigestReference("egress", dest)
	decID := cmddiag.DigestReference("egress_decision", sess, inv, dest)

	dec := cmddiag.WEAEgressDecisionFact{
		DecisionID:     decID,
		SessionID:      sess,
		ProjectID:      strings.TrimSpace(cmd.ProjectID),
		InvocationID:   inv,
		DestinationRef: dest,
		GrantScope:     "exact_action",
		MaxUses:        1,
		Admitted:       allowed,
		Refused:        !allowed,
		DecisionSeq:    seq,
		SourceRef:      destDigest,
	}
	if !allowed {
		dec.RefusalCode = "HOST_DENIED"
	}

	obs := cmddiag.WEAObservation{
		SessionID:       sess,
		ProjectID:       strings.TrimSpace(cmd.ProjectID),
		InvocationID:    inv,
		ScopeKey:        decID,
		Settled:         true,
		EgressDecisions: []cmddiag.WEAEgressDecisionFact{dec},
	}
	if allowed {
		connSeq := egressWEADiagSeq.Add(1)
		obs.ObservedConnections = []cmddiag.WEAEgressConnectionFact{
			{
				ConnectionID:   cmddiag.DigestReference("egress_conn", sess, inv, dest),
				SessionID:      sess,
				ProjectID:      strings.TrimSpace(cmd.ProjectID),
				InvocationID:   inv,
				DestinationRef: dest,
				ObservedSeq:    connSeq,
				Settled:        true,
				SourceRef:      destDigest,
			},
		}
	}
	return cmddiag.ObserveWEA(obs)
}
