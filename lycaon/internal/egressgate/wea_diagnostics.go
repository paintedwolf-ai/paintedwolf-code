package egressgate

import (
	"net"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

func init() {
	confine.SetBrokerConnectionObserver(func(cmd confine.EgressCommand, ep egressproxy.Endpoint) {
		observeBrokerConnectionWEA(cmd, ep)
	})
}

// observeEgressDecisionWEA emits ONLY the real owning policy/decision record
// (EgressDecisions) for an attributed egress gate decision. It deliberately
// does NOT fabricate ObservedConnections: actual connection observations come
// independently from the transport/broker observer in internal/confine via
// SetBrokerConnectionObserver.
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
	decID := cmddiag.DigestReference("egress_decision", sess, inv, dest, strconv.FormatInt(seq, 10))

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

// observeBrokerConnectionWEA emits ONLY the independently observed upstream
// connection fact forwarded from confine's egress broker dial outcome callback.
// Policy decisions are emitted independently by observeEgressDecisionWEA and
// joined inside cmddiag.Observer by stable identity and monotonic sequence.
// When diagnostics are disabled, this returns immediately with zero allocations.
func observeBrokerConnectionWEA(cmd confine.EgressCommand, ep egressproxy.Endpoint) bool {
	if !cmddiag.Enabled() {
		return false
	}
	sess := strings.TrimSpace(cmd.SessionID)
	host := strings.ToLower(strings.TrimSpace(ep.Host))
	if sess == "" || host == "" {
		return false
	}
	dest := host
	if ep.Port > 0 {
		dest = net.JoinHostPort(host, strconv.Itoa(int(ep.Port)))
	}
	inv := strings.TrimSpace(cmd.ActionID)
	if inv == "" {
		inv = strings.TrimSpace(cmd.ToolCallID)
	}
	if inv == "" {
		inv = sess
	}
	seq := cmddiag.NextWEASeq()
	connID := cmddiag.DigestReference("broker_conn", sess, inv, dest, strconv.FormatInt(seq, 10))
	return cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID:    sess,
		ProjectID:    strings.TrimSpace(cmd.ProjectID),
		InvocationID: inv,
		ScopeKey:     connID,
		Settled:      true,
		ObservedConnections: []cmddiag.WEAEgressConnectionFact{
			{
				ConnectionID:   connID,
				SessionID:      sess,
				ProjectID:      strings.TrimSpace(cmd.ProjectID),
				InvocationID:   inv,
				DestinationRef: dest,
				ObservedSeq:    seq,
				Settled:        true,
				SourceRef:      cmddiag.DigestReference("confine_broker", dest),
			},
		},
	})
}
