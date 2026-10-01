package tools

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostscope"
)

// egressAskSubject binds card content and approval identity to one endpoint or declared destination set.
type egressAskSubject struct {
	Args        map[string]any
	Title       string
	Impact      string
	Explanation *hitl.ApprovalExplanation
	// Declared is the typed destination-set payload, non-nil only for a set ask.
	Declared *hitl.DeclaredEndpoints
	// Contained carries the boundary permit for a declared set so the grant
	// witness and GrantKey identify the set, not one of its members.
	Contained hitl.Contained
	// ApprovalKey overrides action-digest identity when the ask is not identified
	// by its args alone (a detection cites a rule; a set cites its digest).
	ApprovalKey string
}

// destinationFact projects one observed endpoint onto the gate's view. Opaque is
// a transport fact: CONNECT and SOCKS carry unreadable streams; HTTP is parsed here.
func destinationFact(cmd confine.EgressCommand, ep egressproxy.Endpoint, firstUse bool, configured, registry string) *gate.Endpoint {
	tunnelled, _ := egressTunnelTransport(ep.Transport)
	return &gate.Endpoint{
		Host:                ep.Host,
		Port:                ep.Port,
		Transport:           string(ep.Transport),
		Configured:          cmd.DeclaresHost(ep.Host) || configured != "",
		ConfiguredBy:        configured,
		PublicRegistry:      registry,
		Opaque:              tunnelled,
		FirstUseThisSession: firstUse,
	}
}

// endpointAskSubject builds the ask for one observed endpoint.
func endpointAskSubject(cmd confine.EgressCommand, ep egressproxy.Endpoint) egressAskSubject {
	host := ep.Host
	s := egressAskSubject{
		Args:      map[string]any{"host": host, "transport": string(ep.Transport)},
		Title:     "Allow network: " + host,
		Impact:    "Outbound requests to " + host,
		Contained: egressContained(cmd),
		Explanation: &hitl.ApprovalExplanation{
			What:    "Connect to " + host,
			Who:     hitl.WhoAgentCommand,
			IfWrong: "It could upload data to or fetch code from this host.",
			// Reusable grants cover the registrable domain shown here.
			AllowLine: hostscope.AllowLine(host),
		},
	}
	if tunnelled, verb := egressTunnelTransport(ep.Transport); tunnelled {
		// Tunnel grants include the destination port because their contents are opaque.
		s.Args["port"] = ep.Port
		s.Title = fmt.Sprintf("Allow network: %s:%d", host, ep.Port)
		s.Impact = fmt.Sprintf("Outbound %s connection to %s on port %d through network mediation", verb, host, ep.Port)
		s.Explanation.What = fmt.Sprintf("Open a %s tunnel to %s on port %d", verb, host, ep.Port)
		s.Explanation.Who = hitl.WhoAgentCommand + " over mediated TCP"
		s.Explanation.IfWrong = "The tunnel's contents are not visible to the app — it could carry anything to this destination."
		s.Explanation.AllowLine = fmt.Sprintf("%s connections to %s on port %d", verb, host, ep.Port)
	}
	return s
}

// egressTunnelTransport reports whether a transport hands the broker an opaque
// byte stream, and the label the card uses for it. Opacity is the transport's own
// answer; only the label lives here.
func egressTunnelTransport(t egressproxy.Transport) (tunnelled bool, verb string) {
	if !t.Opaque() {
		return false, ""
	}
	if t == egressproxy.TransportSocksTCP {
		return true, "SOCKS5"
	}
	return true, "TLS/CONNECT"
}

// declaredSetAskSubject builds the ask for a whole host-declared destination set,
// so one fan-out is one decision.
func declaredSetAskSubject(cmd confine.EgressCommand) egressAskSubject {
	hosts := confine.NormalizeDeclaredHosts(cmd.DeclaredHosts)
	digest, count := confine.DeclaredHostsDigest(hosts)
	noun := "endpoints"
	if count == 1 {
		noun = "endpoint"
	}
	countedNoun := strconv.Itoa(count) + " configured " + noun

	contained := egressContained(cmd)
	contained.DeclaredHostsDigest = digest
	contained.DeclaredHostCount = count

	return egressAskSubject{
		Args: map[string]any{
			"hosts":                 hosts,
			"host_count":            count,
			"declared_hosts_digest": digest,
		},
		Title:     "Allow network: " + countedNoun,
		Impact:    "Outbound network connections to the " + countedNoun + " this tool call reaches",
		Contained: contained,
		Declared: &hitl.DeclaredEndpoints{
			Hosts:     hosts,
			HostCount: count,
			Digest:    digest,
			Source:    hitl.DeclaredEndpointProviderCatalog,
		},
		Explanation: &hitl.ApprovalExplanation{
			What:      "Connect to the " + countedNoun + " this tool call reaches",
			Who:       hitl.WhoAgentCommand,
			IfWrong:   "These destinations come from your configuration, but the content sent to them does not.",
			AllowLine: "connections to these " + countedNoun,
		},
		ApprovalKey: "egress-declared:" + digest,
	}
}

// egressContained is the containment fact shared by every egress ask. Egress asks
// are raised from the proxy path, where the write jail is already applied.
func egressContained(cmd confine.EgressCommand) hitl.Contained {
	return hitl.Contained{
		FSJailed: true,
		Egress:   hitl.ContainedEgressProxy,
		Roots:    []string{cmd.ProjectDir},
	}
}

// withDetection pins the approval identity to the exact action and rule, so an
// unrelated host grant cannot silence a detection hold and two different rules on
// one host stay two decisions.
func (s egressAskSubject) withDetection(
	cmd confine.EgressCommand,
	ep egressproxy.Endpoint,
	detection *confine.EgressDetectionCitation,
) egressAskSubject {
	if detection == nil {
		return s
	}
	title := detection.RuleTitle
	if title == "" {
		title = detection.RuleID
	}
	if ep.Transport == egressproxy.TransportSocksTCP {
		s.Explanation.What = fmt.Sprintf("Connect to %s on port %d — matches detection %q", ep.Host, ep.Port, title)
	} else {
		s.Explanation.What = "Connect to " + ep.Host + " — matches detection \"" + title + "\""
	}
	fields := []string{cmd.SessionID, cmd.ProjectDir, cmd.ToolCallID, cmd.Image, cmd.CommandLine, ep.Host}
	if ep.Transport == egressproxy.TransportSocksTCP {
		fields = append(fields, strconv.Itoa(int(ep.Port)), string(ep.Transport))
	}
	fields = append(fields, detection.PackID, detection.RuleID, detection.Level)
	digest := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	s.ApprovalKey = fmt.Sprintf("egress-detection:%x", digest[:16])
	return s
}

// egressAskFor builds the subject for one parked connection: a declared set when
// the endpoint belongs to one, otherwise the single endpoint, with detection
// identity applied when a pack matched.
func egressAskFor(
	cmd confine.EgressCommand,
	ep egressproxy.Endpoint,
	detection *confine.EgressDetectionCitation,
) egressAskSubject {
	if len(cmd.DeclaredHosts) > 0 && cmd.DeclaresHost(ep.Host) {
		return declaredSetAskSubject(cmd).withDetection(cmd, ep, detection)
	}
	return endpointAskSubject(cmd, ep).withDetection(cmd, ep, detection)
}
