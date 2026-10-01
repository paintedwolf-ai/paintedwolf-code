// Package egressproxy mediates outbound connections for confined commands.
//
// One broker serves the host at a stable address: a process that outlives its
// command still reaches a listener that answers, and the address a confined
// process reads is the same on every invocation, so build caches can key on it.
//
// Identity does not travel in the environment. The broker resolves the process
// on the far end of each connection to the action whose descendants it belongs
// to. A caller it cannot place, or one whose action ended, is refused in terms
// the host records.
package egressproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// BlockedMarker identifies a denied proxy request.
const BlockedMarker = "LYCAON_SANDBOX_BLOCKED"

// Peer is the confined process on the far end of a broker connection.
type Peer struct {
	PID int
	// Lineage identifies the launch this process descends from.
	Lineage string
	// Leased reports whether an action is accountable for this connection.
	Leased bool
	// Owner names the action the process descends from.
	Owner string
	// Inherited marks a connection from a process an earlier action left running,
	// which a live action of the same project answers for. The destination still
	// crosses that action's gate; only its provenance differs.
	Inherited bool
}

// PeerResolver answers which lineage owns a loopback connection. ok is false
// when the host cannot identify the calling process at all.
type PeerResolver func(local, remote netip.AddrPort) (Peer, bool)

// Refusal names why the broker turned a caller away before deciding any
// destination. Each is recorded, so a refused dial is never silent.
type Refusal string

const (
	// RefusedUnattributable means the host could not identify the caller.
	RefusedUnattributable Refusal = "egress_unattributable"
	// RefusedLeaseEnded means the caller descends from an action that ended.
	RefusedLeaseEnded Refusal = "egress_lease_ended"
)

// RefusalReporter records a caller the broker turned away.
type RefusalReporter func(peer Peer, ep Endpoint, refusal Refusal)

// EndpointDecider returns whether the lineage may connect to ep.
type EndpointDecider func(ctx context.Context, lineage string, ep Endpoint) bool

// LoopbackConnectAuthority checks one lineage's local port grant.
type LoopbackConnectAuthority func(lineage string, port uint16) bool

// LeaseCheck reports whether an owner still holds this lineage. The broker
// rechecks it around waits, because an approval can outlive the action.
type LeaseCheck func(lineage string) bool

// ListenFunc binds a listener for tests.
type ListenFunc func(network, address string) (net.Listener, error)

// Options configures listener factories and the preferred stable ports.
type Options struct {
	ListenHTTP  ListenFunc
	ListenSOCKS ListenFunc
	// HTTPPort and SOCKSPort are the recorded ports. Zero, or a port another
	// process holds, binds a fresh one.
	HTTPPort  uint16
	SOCKSPort uint16
}

// Addrs holds the HTTP and SOCKS listener addresses.
type Addrs struct {
	HTTP  string
	SOCKS string
}

// DialReporter receives nil on connection success or the dial error.
type DialReporter func(lineage string, ep Endpoint, dialErr error)

// HTTPReporter receives the terminal HTTP response status, or the transport
// error when no response was received. CONNECT and SOCKS do not expose this.
type HTTPReporter func(lineage string, ep Endpoint, statusCode int, requestErr error)

// Broker is the host's loopback forward proxy.
type Broker struct {
	resolvePeer    PeerResolver
	decideEndpoint EndpointDecider
	loopbackGrant  LoopbackConnectAuthority
	leaseLive      LeaseCheck
	reportRefusal  RefusalReporter
	dialEndpoint   DialEndpoint
	reportDial     DialReporter
	reportHTTP     HTTPReporter
	httpLn         net.Listener
	socksLn        net.Listener
	server         *http.Server
	serveCtx       context.Context
	cancel         context.CancelFunc
	serveWG        sync.WaitGroup
	connWG         sync.WaitGroup
}

// New constructs the host broker. Without a peer resolver it serves nobody.
func New(decide EndpointDecider) *Broker {
	return &Broker{decideEndpoint: decide}
}

// SetPeerResolver wires caller identification.
func (b *Broker) SetPeerResolver(fn PeerResolver) {
	if b != nil {
		b.resolvePeer = fn
	}
}

// SetDialReporter wires dial-outcome observation (nil disables).
func (b *Broker) SetDialReporter(fn DialReporter) {
	if b != nil {
		b.reportDial = fn
	}
}

// SetHTTPReporter wires plain-HTTP response observation (nil disables).
func (b *Broker) SetHTTPReporter(fn HTTPReporter) {
	if b != nil {
		b.reportHTTP = fn
	}
}

// SetLoopbackConnectAuthority wires per-lineage loopback-connect grants.
// Without one the broker relays nothing that resolves to this machine.
func (b *Broker) SetLoopbackConnectAuthority(fn LoopbackConnectAuthority) {
	if b != nil {
		b.loopbackGrant = fn
	}
}

// SetLeaseCheck wires the recheck used around approval waits and before each
// dial. Without one, a resolved peer counts as still leased.
func (b *Broker) SetLeaseCheck(fn LeaseCheck) {
	if b != nil {
		b.leaseLive = fn
	}
}

// SetRefusalReporter wires observation of callers turned away before any
// destination decision (nil disables).
func (b *Broker) SetRefusalReporter(fn RefusalReporter) {
	if b != nil {
		b.reportRefusal = fn
	}
}

// SetDialEndpointForTest overrides upstream dialing in tests.
func (b *Broker) SetDialEndpointForTest(d DialEndpoint) { b.dialEndpoint = d }

// Start binds both front doors and serves them until Close, preferring the
// recorded ports so an already-running descendant keeps reaching them.
func (b *Broker) Start(opts ...Options) (Addrs, error) {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	listenHTTP := o.ListenHTTP
	if listenHTTP == nil {
		listenHTTP = net.Listen
	}
	listenSOCKS := o.ListenSOCKS
	if listenSOCKS == nil {
		listenSOCKS = listenHTTP
	}

	httpLn, err := listenPreferred(listenHTTP, o.HTTPPort)
	if err != nil {
		return Addrs{}, err
	}
	socksLn, err := listenPreferred(listenSOCKS, o.SOCKSPort)
	if err != nil {
		_ = httpLn.Close()
		return Addrs{}, fmt.Errorf("socks listen: %w", err)
	}

	b.httpLn = httpLn
	b.socksLn = socksLn
	b.serveCtx, b.cancel = context.WithCancel(context.Background())
	b.server = &http.Server{
		Handler:           http.HandlerFunc(b.handle),
		ReadHeaderTimeout: 30 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, connKey{}, c)
		},
	}

	b.serveWG.Add(2)
	go func() {
		defer b.serveWG.Done()
		_ = b.server.Serve(httpLn)
	}()
	go func() {
		defer b.serveWG.Done()
		b.serveSOCKS(b.serveCtx, socksLn)
	}()

	return Addrs{HTTP: httpLn.Addr().String(), SOCKS: socksLn.Addr().String()}, nil
}

// listenPreferred keeps the recorded port when it is still free.
func listenPreferred(listen ListenFunc, port uint16) (net.Listener, error) {
	if port != 0 {
		if ln, err := listen("tcp", netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), port).String()); err == nil {
			return ln, nil
		}
	}
	return listen("tcp", "127.0.0.1:0")
}

// Addr returns the loopback host:port commands point HTTP(S)_PROXY at.
func (b *Broker) Addr() string {
	if b == nil || b.httpLn == nil {
		return ""
	}
	return b.httpLn.Addr().String()
}

// SocksAddr returns the SOCKS5 listener address.
func (b *Broker) SocksAddr() string {
	if b == nil || b.socksLn == nil {
		return ""
	}
	return b.socksLn.Addr().String()
}

// Close stops both listeners and waits for active connections and serve loops.
func (b *Broker) Close() error {
	if b.cancel != nil {
		b.cancel()
	}
	var err error
	if b.server != nil {
		err = b.server.Close()
	}
	if b.socksLn != nil {
		if closeErr := b.socksLn.Close(); err == nil {
			err = closeErr
		}
	}
	b.serveWG.Wait()
	b.connWG.Wait()
	return err
}

type connKey struct{}

// identify resolves the caller, or states why it cannot be served. Attribution
// settles before any destination is considered.
func (b *Broker) identify(conn net.Conn) (Peer, Refusal) {
	if conn == nil || b.resolvePeer == nil {
		return Peer{}, RefusedUnattributable
	}
	local, localOK := addrPort(conn.LocalAddr())
	remote, remoteOK := addrPort(conn.RemoteAddr())
	if !localOK || !remoteOK {
		return Peer{}, RefusedUnattributable
	}
	peer, ok := b.resolvePeer(local, remote)
	if !ok {
		return Peer{}, RefusedUnattributable
	}
	if !peer.Leased {
		return peer, RefusedLeaseEnded
	}
	return peer, ""
}

func addrPort(addr net.Addr) (netip.AddrPort, bool) {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp == nil {
		return netip.AddrPort{}, false
	}
	parsed, ok := netip.AddrFromSlice(tcp.IP)
	if !ok {
		return netip.AddrPort{}, false
	}
	if tcp.Port < 0 || tcp.Port > 65535 {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(parsed.Unmap(), uint16(tcp.Port)), true
}

func (b *Broker) noteRefusal(peer Peer, ep Endpoint, refusal Refusal) {
	if b.reportRefusal != nil {
		b.reportRefusal(peer, ep, refusal)
	}
}

// refusalMessage names the command that left the process running.
func refusalMessage(peer Peer, refusal Refusal) string {
	switch refusal {
	case RefusedLeaseEnded:
		owner := strings.TrimSpace(peer.Owner)
		if owner == "" {
			owner = "a command that has since finished"
		}
		return BlockedMarker + ": " + string(refusal) +
			"; this process was left running by " + owner +
			" and its network authority ended with that command"
	default:
		return BlockedMarker + ": " + string(RefusedUnattributable) +
			"; this proxy only serves processes the app started"
	}
}

func (b *Broker) handle(w http.ResponseWriter, r *http.Request) {
	conn, _ := r.Context().Value(connKey{}).(net.Conn)
	peer, refusal := b.identify(conn)
	r.Header.Del("Proxy-Authorization")
	if r.Method == http.MethodConnect {
		b.handleConnect(w, r, peer, refusal)
		return
	}
	if r.URL == nil || !strings.EqualFold(r.URL.Scheme, "http") || r.URL.Host == "" {
		http.Error(w, BlockedMarker+": invalid forward-proxy request", http.StatusBadRequest)
		return
	}
	ep, err := ParseHTTPEndpoint(r.URL.Host, TransportHTTPRequest)
	if err != nil {
		http.Error(w, BlockedMarker+": invalid destination", http.StatusBadRequest)
		return
	}
	ep.RequestMethod = strings.ToUpper(r.Method)
	ep.RequestPath = r.URL.EscapedPath()
	if ep.RequestPath == "" {
		ep.RequestPath = "/"
	}
	ep.Inherited = peer.Inherited
	if refusal == "" && !b.stillLeased(peer.Lineage) {
		refusal = RefusedLeaseEnded
	}
	if refusal != "" {
		b.noteRefusal(peer, ep, refusal)
		writeBrokerRefusal(w, peer, refusal)
		return
	}
	if !b.allowedEndpoint(r.Context(), peer.Lineage, ep) {
		http.Error(w, BlockedMarker+": egress to this host is not allowed; review it in Settings → Approvals", http.StatusForbidden)
		return
	}
	// Approval waits may outlive the action lease.
	if !b.stillLeased(peer.Lineage) {
		b.noteRefusal(peer, ep, RefusedLeaseEnded)
		writeBrokerRefusal(w, peer, RefusedLeaseEnded)
		return
	}
	b.forwardHTTP(w, r, peer.Lineage, ep)
}

func (b *Broker) handleConnect(w http.ResponseWriter, r *http.Request, peer Peer, refusal Refusal) {
	ep, err := ParseHTTPEndpoint(r.Host, TransportHTTPConnect)
	if err != nil {
		http.Error(w, BlockedMarker+": invalid destination", http.StatusBadRequest)
		return
	}
	ep.Inherited = peer.Inherited
	if refusal == "" && !b.stillLeased(peer.Lineage) {
		refusal = RefusedLeaseEnded
	}
	if refusal != "" {
		b.noteRefusal(peer, ep, refusal)
		writeBrokerRefusal(w, peer, refusal)
		return
	}
	if !b.allowedEndpoint(r.Context(), peer.Lineage, ep) {
		http.Error(w, BlockedMarker+": egress to this host is not allowed; review it in Settings → Approvals", http.StatusForbidden)
		return
	}
	upstream, err := b.dialAuthorizedEndpoint(r.Context(), peer.Lineage, ep)
	if err != nil {
		if errors.Is(err, errCommandAuthorizationRevoked) {
			b.noteRefusal(peer, ep, RefusedLeaseEnded)
			writeBrokerRefusal(w, peer, RefusedLeaseEnded)
			return
		}
		if writeDialRefusal(w, err) {
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "proxy cannot hijack", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	relayCtx, cancel := b.relayContext(r.Context())
	defer cancel()
	relayHalfClose(relayCtx, client, upstream)
}

func (b *Broker) forwardHTTP(w http.ResponseWriter, r *http.Request, lineage string, approved Endpoint) {
	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	removeHopByHopHeaders(outReq.Header)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Direct dialing keeps destination authorization within this broker.
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, fmt.Errorf("unsupported network %q", network)
		}
		ep, err := ParseHTTPEndpoint(address, TransportHTTPRequest)
		if err != nil {
			return nil, err
		}
		if ep.Host != approved.Host || ep.Port != approved.Port {
			return nil, fmt.Errorf("blocked: transport requested unexpected destination")
		}
		return b.dialAuthorizedEndpoint(ctx, lineage, approved)
	}
	defer transport.CloseIdleConnections()
	resp, err := transport.RoundTrip(outReq)
	if err != nil {
		if b.reportHTTP != nil && !errors.Is(err, errCommandAuthorizationRevoked) {
			b.reportHTTP(lineage, approved, 0, err)
		}
		if errors.Is(err, errCommandAuthorizationRevoked) {
			writeBrokerRefusal(w, Peer{Lineage: lineage}, RefusedLeaseEnded)
			return
		}
		if writeDialRefusal(w, err) {
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if b.reportHTTP != nil {
		b.reportHTTP(lineage, approved, resp.StatusCode, nil)
	}
	defer func() { _ = resp.Body.Close() }()
	removeHopByHopHeaders(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (b *Broker) loopbackAuthorized(lineage string, port uint16) bool {
	return b != nil && b.loopbackGrant != nil && b.loopbackGrant(lineage, port)
}

func (b *Broker) dialAuthorizedEndpoint(ctx context.Context, lineage string, ep Endpoint) (net.Conn, error) {
	conn, err := b.dialAuthorizedEndpointInner(ctx, lineage, ep)
	// A revoked authorization is policy, not a connect outcome.
	if b.reportDial != nil && !errors.Is(err, errCommandAuthorizationRevoked) {
		b.reportDial(lineage, ep, err)
	}
	return conn, err
}

// stillLeased reports whether an owner is still accountable for the lineage.
func (b *Broker) stillLeased(lineage string) bool {
	return b.leaseLive == nil || b.leaseLive(lineage)
}

func (b *Broker) dialAuthorizedEndpointInner(ctx context.Context, lineage string, ep Endpoint) (net.Conn, error) {
	leased := func() bool { return b.stillLeased(lineage) }
	if !leased() {
		return nil, errCommandAuthorizationRevoked
	}
	if b.dialEndpoint == nil {
		return defaultDialEndpoint(ctx, ep, leased, b.loopbackAuthorized(lineage, ep.Port))
	}
	conn, err := b.dialEndpoint(ctx, ep)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, fmt.Errorf("dial endpoint returned no connection")
	}
	if !leased() {
		_ = conn.Close()
		return nil, errCommandAuthorizationRevoked
	}
	return conn, nil
}

func removeHopByHopHeaders(header http.Header) {
	for _, connection := range header.Values("Connection") {
		for name := range strings.SplitSeq(connection, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{
		"Connection",
		"Proxy-Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		header.Del(name)
	}
}

// writeDialRefusal returns true after rendering a loopback authorization refusal.
func writeDialRefusal(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, ErrLoopbackNotAuthorized) {
		return false
	}
	http.Error(w, BlockedMarker+": "+err.Error()+
		"; reaching a service on this machine needs an approved loopback_connect capability",
		http.StatusForbidden)
	return true
}

func writeBrokerRefusal(w http.ResponseWriter, peer Peer, refusal Refusal) {
	http.Error(w, refusalMessage(peer, refusal), http.StatusForbidden)
}

func (b *Broker) relayContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if b.serveCtx == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(b.serveCtx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
