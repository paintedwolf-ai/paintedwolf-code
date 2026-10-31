package loopwake

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/outboundhttp"
)

const (
	conditionProbeStart = 250 * time.Millisecond
	conditionProbeMax   = 2 * time.Second
)

func startConditionMonitor(ctx context.Context, loop *WaitSubscriptions, store *awaitstore.Store, lease awaitstore.Lease) {
	if loop == nil || store == nil || (!hasActiveCondition(lease.Conditions) && !(lease.UntilComplete && hasConditionKind(lease.Conditions, WaitTriggerProcessDone))) {
		return
	}
	go monitorConditions(ctx, loop, store, lease)
}

func hasActiveCondition(conditions []awaitstore.Condition) bool {
	return hasConditionKind(conditions, WaitTriggerHTTPReady) || hasConditionKind(conditions, WaitTriggerPortReady)
}

func hasConditionKind(conditions []awaitstore.Condition, kind WaitTrigger) bool {
	for _, condition := range conditions {
		if condition.Kind == string(kind) {
			return true
		}
	}
	return false
}

func monitorConditions(ctx context.Context, loop *WaitSubscriptions, store *awaitstore.Store, lease awaitstore.Lease) {
	defer confine.ForgetEgressAction(lease.SessionID, lease.ToolCallID)
	delay := conditionProbeStart
	for {
		if reconcileWaitConditions(ctx, loop, store, lease) {
			return
		}
		if !hasActiveCondition(lease.Conditions) {
			delay = 30 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if delay < conditionProbeMax {
			delay *= 2
			if delay > conditionProbeMax {
				delay = conditionProbeMax
			}
		}
	}
}

// Reconciliation stops only for a terminal lease or a completed settlement.
// Store failures leave the host monitor alive for a later retry.
func reconcileWaitConditions(ctx context.Context, loop *WaitSubscriptions, store *awaitstore.Store, lease awaitstore.Lease) bool {
	if ctx.Err() != nil {
		return true
	}
	active, ok, err := store.ForSession(ctx, lease.SessionID)
	if err != nil {
		return false
	}
	if !ok || active.ID != lease.ID || waitDeadlineExpired(lease) {
		return true
	}
	for _, condition := range lease.Conditions {
		winner, terminal := conditionOutcome(ctx, lease, condition)
		if lease.UntilComplete && condition.Kind == "process_done" {
			winner, terminal = loop.processConditionOutcome(lease.SessionID, condition)
		}
		if !terminal {
			continue
		}
		if waitDeadlineExpired(lease) {
			return true
		}
		_, err := loop.resolveLease(ctx, store, lease, winner)
		return err == nil
	}
	return false
}

func waitDeadlineExpired(lease awaitstore.Lease) bool {
	return !lease.Deadline.IsZero() && !time.Now().UTC().Before(lease.Deadline)
}

func conditionOutcome(ctx context.Context, lease awaitstore.Lease, condition awaitstore.Condition) (awaitstore.Condition, bool) {
	switch condition.Kind {
	case "http_ready":
		return httpConditionOutcome(ctx, lease, condition)
	case "port_ready":
		if portConditionSatisfied(ctx, lease, condition) {
			condition.Outcome = "satisfied"
			return condition, true
		}
	default:
	}
	return awaitstore.Condition{}, false
}

func httpConditionOutcome(ctx context.Context, lease awaitstore.Lease, condition awaitstore.Condition) (awaitstore.Condition, bool) {
	cmd := confine.EgressCommand{
		SessionID: lease.SessionID, RootSessionID: lease.RootSessionID, ProjectID: lease.ProjectID,
		ProjectDir: lease.ProjectDir, ToolCallID: lease.ToolCallID, Image: "wait",
	}
	requestCtx := egressgate.WithAttribution(ctx, cmd)
	response, err := outboundhttp.Do(requestCtx, outboundhttp.Request{
		Class: egressclass.AgentHTTPRequest, Method: condition.Method, URL: condition.URL,
		Timeout: 2 * time.Second, Redirects: outboundhttp.RedirectNone, DiscardBody: true,
		AllowAddress: loopbackAddressPolicy(lease.LoopbackPorts),
		BeforeHop: func(hopCtx context.Context, hopURL *url.URL) error {
			return egressgate.AwaitHTTPRequest(hopCtx, hopURL, condition.Method)
		},
	})
	if err == nil && response.Status >= condition.StatusMin && response.Status <= condition.StatusMax {
		condition.Outcome = "satisfied"
		return condition, true
	}
	if _, denied := egressgate.Denied(requestCtx); denied {
		condition.Outcome = "denied"
		condition.Code = "HTTP_REQUEST_HOST_DENIED"
		return condition, true
	}
	return awaitstore.Condition{}, false
}

func portConditionSatisfied(ctx context.Context, lease awaitstore.Lease, condition awaitstore.Condition) bool {
	if !portCovered(lease.LoopbackPorts, condition.Port) {
		return false
	}
	lookupCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(lookupCtx, "ip", condition.Host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	for _, addr := range addrs {
		if !addr.IsLoopback() {
			return false
		}
		conn, dialErr := dialer.DialContext(ctx, "tcp", net.JoinHostPort(addr.String(), strconv.Itoa(int(condition.Port))))
		if dialErr == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}

func loopbackAddressPolicy(ports []uint16) func(netip.Addr, uint16) bool {
	return func(addr netip.Addr, port uint16) bool {
		return addr.IsLoopback() && portCovered(ports, port)
	}
}

func portCovered(ports []uint16, port uint16) bool {
	for _, allowed := range ports {
		if allowed == port {
			return true
		}
	}
	return false
}
