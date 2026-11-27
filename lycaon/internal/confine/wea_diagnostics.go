package confine

import (
	"net"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

// observeBrokerConnectionWEA emits ONLY the independently observed upstream
// connection fact from the egress broker's dial/HTTP outcome callbacks.
// Policy decisions are emitted independently by egressgate and joined inside
// cmddiag.Observer by stable identity and monotonic sequence.
// When diagnostics are disabled, this returns immediately with zero allocations.
func observeBrokerConnectionWEA(cmd EgressCommand, ep egressproxy.Endpoint) bool {
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
