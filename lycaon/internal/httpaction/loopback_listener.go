package httpaction

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type loopbackHopObservation struct {
	preByFamily map[bool]*api.LoopbackListenerFact
	post        *api.LoopbackListenerFact
	peer        netip.AddrPort
}

func (o *loopbackHopObservation) beforeFirstHop(ctx context.Context, tctx tools.ToolContext, spec requestSpec) {
	addr, port, ok := spec.firstHop()
	if !ok {
		return
	}
	if o.preByFamily == nil {
		o.preByFamily = make(map[bool]*api.LoopbackListenerFact, 2)
	}
	_, _, mapped := outboundhttp.MappedDial(spec.resolve, spec.target.Hostname(), port)
	if !mapped && spec.target != nil && strings.EqualFold(spec.target.Hostname(), "localhost") {
		v4 := netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), port)
		v6 := netip.AddrPortFrom(netip.AddrFrom16([16]byte{15: 1}), port)
		o.preByFamily[false] = observeLoopbackEndpoint(ctx, tctx, v4)
		o.preByFamily[true] = observeLoopbackEndpoint(ctx, tctx, v6)
		return
	}
	ap := netip.AddrPortFrom(addr.Unmap(), port)
	o.preByFamily[ap.Addr().Is6()] = observeLoopbackEndpoint(ctx, tctx, ap)
}

func (o *loopbackHopObservation) onFirstHopConn(ctx context.Context, tctx tools.ToolContext, _, remote netip.AddrPort) {
	if !remote.IsValid() {
		return
	}
	ip := remote.Addr().Unmap()
	if !ip.IsLoopback() {
		return
	}
	o.peer = netip.AddrPortFrom(ip, remote.Port())
	o.post = observeLoopbackEndpoint(ctx, tctx, o.peer)
}

func (o *loopbackHopObservation) settle(
	ctx context.Context,
	tctx tools.ToolContext,
	spec requestSpec,
	resp outboundhttp.Response,
) *api.LoopbackListenerFact {
	peer := o.peer
	if !peer.IsValid() {
		peer = resp.FirstHopPeer
	}
	if !peer.IsValid() {
		if addr, port, ok := spec.firstHop(); ok {
			peer = netip.AddrPortFrom(addr.Unmap(), port)
		}
	}
	if !peer.IsValid() {
		return nil
	}
	peer = netip.AddrPortFrom(peer.Addr().Unmap(), peer.Port())
	if !peer.Addr().IsLoopback() {
		return nil
	}
	pre := o.preByFamily[peer.Addr().Is6()]
	post := o.post
	if post == nil {
		post = observeLoopbackEndpoint(ctx, tctx, peer)
	}
	if pre == nil && post == nil {
		return nil
	}
	if post == nil || (len(post.Holders) == 0 && pre != nil && len(pre.Holders) > 0) {
		cp := *pre
		cp.ConnectedAddress = peer.String()
		return &cp
	}
	if pre != nil && len(pre.Holders) > 0 && len(post.Holders) > 0 && !sameListenerHolders(pre.Holders, post.Holders) {
		merged := mergeListenerHolders(pre.Holders, post.Holders)
		pub := post.PublishedContainer
		if pub == nil {
			pub = pre.PublishedContainer
		}
		desc := post.Description
		if desc == "" {
			desc = pre.Description
		}
		return &api.LoopbackListenerFact{
			ConnectedAddress:   peer.String(),
			Ambiguous:          true,
			Replaced:           true,
			Holders:            merged,
			ContainerMismatch:  pre.ContainerMismatch || post.ContainerMismatch,
			PublishedContainer: pub,
			Description:        desc,
		}
	}
	cp := *post
	cp.ConnectedAddress = peer.String()
	return &cp
}

func observeLoopbackEndpoint(ctx context.Context, tctx tools.ToolContext, peer netip.AddrPort) *api.LoopbackListenerFact {
	if obs, ok := tctx.Local.ContainerRecorder.(tools.LoopbackEndpointObserver); ok && obs != nil {
		return obs.ObserveLoopbackEndpoint(ctx, tctx.ChatSessionID(), tctx.ActiveRootPath(), peer)
	}
	return observeLoopbackEndpointFallback(tctx, peer)
}

type fallbackSocketListener struct {
	PID      int
	Addr     netip.Addr
	Port     uint16
	IPv6Only bool
}

func observeLoopbackEndpointFallback(tctx tools.ToolContext, peer netip.AddrPort) *api.LoopbackListenerFact {
	port := peer.Port()
	raw, err := lineage.Listeners(lineage.ListenerQuery{
		Protocols: []lineage.Protocol{lineage.ProtocolTCP},
	})
	if err != nil || len(raw) == 0 {
		return nil
	}
	var flat []fallbackSocketListener
	for _, l := range raw {
		if len(l.Holders) == 0 {
			flat = append(flat, fallbackSocketListener{Addr: l.Addr.Addr(), Port: l.Addr.Port(), IPv6Only: l.IPv6Only})
			continue
		}
		for _, h := range l.Holders {
			flat = append(flat, fallbackSocketListener{PID: h.PID, Addr: l.Addr.Addr(), Port: l.Addr.Port(), IPv6Only: l.IPv6Only})
		}
	}
	matched := matchListenersForPeer(flat, peer.Addr().Unmap(), port)
	if len(matched) == 0 {
		return nil
	}
	callerSession := strings.TrimSpace(tctx.Identity.SessionID)
	callerRoot := strings.TrimSpace(tctx.ChatSessionID())
	holders := make([]api.LoopbackListenerHolder, 0, len(matched))
	for _, l := range matched {
		var addrStr string
		if l.Addr.IsValid() {
			addrStr = netip.AddrPortFrom(l.Addr.Unmap(), port).String()
		}
		if l.PID <= 0 {
			holders = append(holders, api.LoopbackListenerHolder{
				Kind:    "unknown",
				Address: addrStr,
			})
			continue
		}
		procName, _ := osprocess.CommandName(l.PID)
		if line, ok := lineage.Of(l.PID); ok && line.Session() != "" {
			sessionID := line.Session()
			lineageID := string(line.ID())
			owner := line.Owner()
			same := (callerSession != "" && sessionID == callerSession) ||
				(callerRoot != "" && sessionID == callerRoot)
			kind := "other_session"
			if same {
				kind = "this_session"
			}
			var cmdStr string
			if cmd, ok := confine.CommandForLineage(lineageID); ok {
				cmdStr = strings.TrimSpace(cmd.CommandLine)
				if cmdStr == "" {
					cmdStr = strings.TrimSpace(cmd.Image)
				}
			}
			if cmdStr == "" {
				cmdStr = procName
			}
			holders = append(holders, api.LoopbackListenerHolder{
				Kind:        kind,
				Address:     addrStr,
				PID:         l.PID,
				ProcessName: procName,
				SessionID:   sessionID,
				Command:     cmdStr,
				Background:  owner == "bg_process",
			})
			continue
		}
		holders = append(holders, api.LoopbackListenerHolder{
			Kind:        "external_process",
			Address:     addrStr,
			PID:         l.PID,
			ProcessName: procName,
			Command:     procName,
		})
	}
	fact := &api.LoopbackListenerFact{
		ConnectedAddress: peer.String(),
		Holders:          holders,
	}
	if len(holders) == 1 {
		h := holders[0]
		fact.Kind = h.Kind
		fact.Address = h.Address
		fact.PID = h.PID
		fact.ProcessName = h.ProcessName
		fact.SessionID = h.SessionID
		fact.Command = h.Command
		fact.Background = h.Background
	} else if len(holders) > 1 {
		fact.Ambiguous = true
	}
	return fact
}

func matchListenersForPeer(all []fallbackSocketListener, peerIP netip.Addr, port uint16) []fallbackSocketListener {
	var onPort []fallbackSocketListener
	hasIPv4 := false
	for _, l := range all {
		if port != l.Port {
			continue
		}
		onPort = append(onPort, l)
		if l.Addr.IsValid() && l.Addr.Unmap().Is4() {
			hasIPv4 = true
		}
	}
	if !peerIP.IsValid() {
		return onPort
	}
	var matched []fallbackSocketListener
	for _, l := range onPort {
		if !l.Addr.IsValid() {
			matched = append(matched, l)
			continue
		}
		la := l.Addr.Unmap()
		if peerIP.Is4() {
			if la.Is4() && (la.IsUnspecified() || la == peerIP) {
				matched = append(matched, l)
				continue
			}
			if la.Is6() && la.IsUnspecified() && !l.IPv6Only && !hasIPv4 {
				matched = append(matched, l)
				continue
			}
		} else if peerIP.Is6() {
			if la.Is6() && (la.IsUnspecified() || la == peerIP) {
				matched = append(matched, l)
				continue
			}
		}
	}
	return matched
}

func sameListenerHolders(a, b []api.LoopbackListenerHolder) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || a[i].PID != b[i].PID || a[i].Address != b[i].Address || a[i].SessionID != b[i].SessionID {
			return false
		}
	}
	return true
}

func mergeListenerHolders(a, b []api.LoopbackListenerHolder) []api.LoopbackListenerHolder {
	out := make([]api.LoopbackListenerHolder, 0, len(a)+len(b))
	seen := make(map[string]bool, len(a)+len(b))
	add := func(h api.LoopbackListenerHolder) {
		key := fmt.Sprintf("%s|%d|%s|%s", h.Kind, h.PID, h.Address, h.SessionID)
		if !seen[key] {
			seen[key] = true
			out = append(out, h)
		}
	}
	for _, h := range a {
		add(h)
	}
	for _, h := range b {
		add(h)
	}
	return out
}

func reportLoopbackListener(tctx tools.ToolContext, fact *api.LoopbackListenerFact) {
	if fact == nil || tctx.Effects.Out == nil {
		return
	}
	tctx.Effects.Out.Listener = fact
	hasOtherSession := fact.Kind == "other_session"
	if !hasOtherSession {
		for _, h := range fact.Holders {
			if h.Kind == "other_session" {
				hasOtherSession = true
				break
			}
		}
	}
	if !hasOtherSession && !fact.ContainerMismatch {
		return
	}
	dest := strings.TrimSpace(fact.ConnectedAddress)
	if dest == "" {
		dest = strings.TrimSpace(fact.Address)
	}
	portStr := ""
	if ap, err := netip.ParseAddrPort(dest); err == nil {
		portStr = strconv.Itoa(int(ap.Port()))
	} else if fact.PublishedContainer != nil && fact.PublishedContainer.HostPort > 0 {
		portStr = strconv.Itoa(fact.PublishedContainer.HostPort)
	}
	detail := strings.TrimSpace(fact.Description)
	if detail == "" {
		if fact.Command != "" && fact.SessionID != "" {
			detail = fmt.Sprintf("Port %s is served by `%s` in chat %s.", portStr, fact.Command, fact.SessionID)
		} else {
			detail = fmt.Sprintf("Port %s is held by another session or competing listener.", portStr)
		}
	}
	tctx.Effects.Out.Facts = tctx.Effects.Out.Facts.WithFeedback(
		toolrejection.HTTPRequestLoopbackOtherSessionCode,
		map[string]any{
			"destination": dest,
			"port":        portStr,
			"detail":      detail,
		},
		&api.FeedbackSubject{Kind: "host", ID: dest},
	)
}
