package confine

import (
	"net"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/isolation"
)

// RefusalContext carries spawn facts needed for refusal guidance.
type RefusalContext struct {
	// MediatedNetwork includes allowed and denied broker observations.
	MediatedNetwork        []EgressHost
	RemotePackageExecution *RemotePackageExecutionBoundary
	// FailedStages are the finished invocation's failed stage command lines;
	// empty while it runs.
	FailedStages []string
	// Running reports an invocation observed before it ended.
	Running bool
	// Refusals is what the kernel reported refusing the invocation.
	Refusals SandboxRefusals
}

// StampedRefusal combines denial guidance and observations.
type StampedRefusal struct {
	Attribution   FailureAttribution
	GuidanceCodes []string
	Observation   Observation
}

// StampRefusal records denials the kernel and the host's egress broker observed.
func StampRefusal(tool, sessionID string, b Boundary, context RefusalContext) StampedRefusal {
	out := StampedRefusal{Observation: Observation{Applied: b.Applied}}
	if !b.Applied {
		return out
	}
	out.Observation.Network = b.Network.Name()
	out.Observation.FailedStages = append([]string(nil), context.FailedStages...)
	out.Observation.Running = context.Running
	out.Observation.Refusals = context.Refusals
	if len(context.Refusals.Refusals) > 0 {
		logSandboxRefusals(tool, sessionID, context.Refusals)
	}
	if b.SocksProxyEnv {
		out.Observation.add(SignalSocksProxy)
	}
	if stampRemotePackageDestination(context, &out) {
		logRefusal(tool, sessionID, out.Observation)
		return out
	}
	for _, host := range context.MediatedNetwork {
		if host.Allowed || strings.TrimSpace(host.Host) == "" {
			continue
		}
		out.Attribution = AttributionSubject
		out.GuidanceCodes = []string{isolation.CodeBoundaryRefused}
		out.Observation.add(SignalBoundaryRefused)
		out.Observation.Destination = deniedDestination(host)
		out.Observation.Port = int(host.Port)
		logRefusal(tool, sessionID, out.Observation)
		break
	}
	return out
}

func stampRemotePackageDestination(context RefusalContext, out *StampedRefusal) bool {
	if out == nil || context.RemotePackageExecution == nil {
		return false
	}
	destination := deniedUndeclaredDestination(
		context.MediatedNetwork, context.RemotePackageExecution.AllowedHosts,
	)
	if destination == "" {
		return false
	}
	out.Attribution = AttributionSubject
	out.Observation.add(SignalBoundaryRefused)
	out.Observation.add(SignalRemotePackageDestinationDenied)
	out.Observation.DenialSubject = DenialSubjectPackageHost
	out.Observation.Destination = destination
	out.GuidanceCodes = []string{isolation.CodeRemotePackageDestinationDenied}
	return true
}

func deniedUndeclaredDestination(network []EgressHost, allowedHosts []string) string {
	allowed := map[string]struct{}{}
	for _, host := range NormalizeDeclaredHosts(allowedHosts) {
		allowed[host] = struct{}{}
	}
	for _, host := range network {
		if host.Allowed || strings.TrimSpace(host.Host) == "" {
			continue
		}
		hostname := strings.ToLower(strings.TrimSpace(host.Host))
		if _, reviewed := allowed[hostname]; reviewed {
			continue
		}
		return deniedDestination(host)
	}
	return ""
}

func deniedDestination(host EgressHost) string {
	hostname := strings.ToLower(strings.TrimSpace(host.Host))
	if host.Port != 0 && host.Port != 443 && host.Port != 80 {
		return net.JoinHostPort(hostname, strconv.Itoa(int(host.Port)))
	}
	return hostname
}
