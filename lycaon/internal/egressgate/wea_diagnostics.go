package egressgate

import (
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

// observeEgressDecisionWEA emits ONLY the real owning policy/decision record
// (EgressDecisions) for an attributed egress gate decision. It deliberately
// does NOT fabricate ObservedConnections: actual connection observations come
// independently from the transport/broker observer in internal/confine.
// When diagnostics are disabled, this returns immediately with zero allocations.
func observeEgressDecisionWEA(cmd confine.EgressCommand, destination string, allowed bool) bool {
	if !cmddiag.Enabled() {
		return false
	}
	sess := strings.TrimSpace(cmd.SessionID)
	dest := strings.ToLower(strings.TrimSpace(destination))
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
	seq := cmddiag.NextWEASeq()
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

	return cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID:       sess,
		ProjectID:       strings.TrimSpace(cmd.ProjectID),
		InvocationID:    inv,
		ScopeKey:        decID,
		Settled:         true,
		EgressDecisions: []cmddiag.WEAEgressDecisionFact{dec},
	})
}
