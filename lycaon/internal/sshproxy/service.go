package sshproxy

import (
	"context"
	"io"
	"net"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/githelper"
)

// MaxActiveConnections caps concurrent handshakes and established streams per service.
const MaxActiveConnections = 16

const (
	replyWriteDeadline = 2 * time.Second
	relayIdleTimeout   = 5 * time.Minute
)

const (
	OutcomeEstablished    = "established"
	OutcomeLeaseEnded     = "lease_ended"
	OutcomeEndpointDenied = "endpoint_denied"
	OutcomeDialFailed     = "dial_failed"
	OutcomeInvalidRequest = "invalid_request"
	OutcomeSetupFailed    = "setup_failed"
)

const (
	StageRequest     = "request"
	StageBrokerSetup = "broker_setup"
	StageLease       = "lease"
	StagePolicy      = "policy"
	StageDial        = "dial"
)

// Attempt records one verified connect attempt in arrival order.
type Attempt struct {
	Ordinal         int
	ExecutionID     string
	Destination     string
	Host            string
	Port            uint16
	Stage           string
	Outcome         string
	Reply           ReplyCode
	PolicyReason    string
	ErrorClass      string
	SawServerBanner bool
	ReachedUserAuth bool
}

// DialResult carries the upstream connection or typed refusal detail for a verified connect request.
type DialResult struct {
	Conn         net.Conn
	Reply        ReplyCode
	Stage        string
	Outcome      string
	PolicyReason string
	ErrorClass   string
}

// DialFunc dials an authorized endpoint on behalf of the service's bound action.
type DialFunc func(ctx context.Context, ep egressproxy.Endpoint) (DialResult, error)

// ServeOptions configures one per-invocation SSH service.
type ServeOptions struct {
	GitBinary   string
	ExecutionID string
	Dial        DialFunc
}

// Service mediates SSH TCP streams for one launched Git invocation over a private Unix socket.
type Service struct {
	cancel    context.CancelFunc
	listener  *net.UnixListener
	directory string
	binding   *githelper.Binding
	dial      DialFunc
	deadline  time.Duration
	accepted  func()

	mu               sync.Mutex
	executionID      string
	active           int
	closed           bool
	conns            map[io.Closer]struct{}
	attempts         []Attempt
	releaseAdmission []func()

	wg        sync.WaitGroup
	closeOnce sync.Once
}

// Serve allocates a private service socket and accepts connections until Close or ctx cancellation.
func Serve(ctx context.Context, options ServeOptions) (*Service, error) {
	return serve(ctx, options, nil)
}

func serve(ctx context.Context, options ServeOptions, configure func(*Service)) (*Service, error) {
	binding, err := githelper.NewBinding(options.GitBinary)
	if err != nil {
		return nil, err
	}
	listener, directory, err := githelper.Listen(ctx, "ssh")
	if err != nil {
		return nil, err
	}
	serviceCtx, cancel := context.WithCancel(ctx)
	s := &Service{
		cancel:      cancel,
		listener:    listener,
		directory:   directory,
		binding:     binding,
		executionID: options.ExecutionID,
		dial:        options.Dial,
		deadline:    githelper.SetupDeadline,
		conns:       make(map[io.Closer]struct{}),
	}
	if configure != nil {
		configure(s)
	}
	s.wg.Add(1)
	go s.accept(serviceCtx)
	context.AfterFunc(ctx, func() { _ = s.Close() })
	return s, nil
}

// HasDialer reports whether the service has an outbound dialer bound.
func (s *Service) HasDialer() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dial != nil
}

// OnClose registers an admission release function invoked once during Close.
func (s *Service) OnClose(fn func()) {
	if fn == nil {
		return
	}
	s.mu.Lock()
	closed := s.closed
	if !closed {
		s.releaseAdmission = append(s.releaseAdmission, fn)
	}
	s.mu.Unlock()
	if closed {
		fn()
	}
}

// SetDial binds the host dial capability before the service is bound to its invocation.
func (s *Service) SetDial(dial DialFunc) {
	s.mu.Lock()
	s.dial = dial
	s.mu.Unlock()
}

// SetExecutionID binds the originating execution identity stamped onto verified attempts.
func (s *Service) SetExecutionID(executionID string) {
	s.mu.Lock()
	s.executionID = executionID
	s.mu.Unlock()
}

// Attempts returns verified connect attempts in the order they were recorded.
func (s *Service) Attempts() []Attempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.attempts)
}

func (s *Service) recordAttempt(attempt Attempt) int {
	s.mu.Lock()
	idx := len(s.attempts)
	attempt.Ordinal = idx + 1
	if attempt.ExecutionID == "" {
		attempt.ExecutionID = s.executionID
	}
	s.attempts = append(s.attempts, attempt)
	s.mu.Unlock()
	return idx
}

func (s *Service) updateAttemptHandshake(idx int, sawServerBanner, reachedUserAuth bool) {
	s.mu.Lock()
	if idx >= 0 && idx < len(s.attempts) {
		s.attempts[idx].SawServerBanner = sawServerBanner
		s.attempts[idx].ReachedUserAuth = reachedUserAuth
	}
	s.mu.Unlock()
}

func (s *Service) currentDial() DialFunc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dial
}

// Bind records the started Git or supervisor PID once and wakes pending handshakes.
func (s *Service) Bind(pid int) error { return s.binding.Bind(pid) }

// BindError returns the error recorded when the first Bind failed, or nil.
func (s *Service) BindError() error { return s.binding.BindError() }

// Path returns the private Unix socket address.
func (s *Service) Path() string { return s.listener.Addr().String() }

// Close stops accepting, closes active connections, wakes pending binds, releases admission and removes the directory.
func (s *Service) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.cancel()
		s.binding.Close()
		err = s.listener.Close()

		s.mu.Lock()
		s.closed = true
		closers := make([]io.Closer, 0, len(s.conns))
		for c := range s.conns {
			closers = append(closers, c)
		}
		releases := s.releaseAdmission
		s.releaseAdmission = nil
		s.mu.Unlock()

		for _, c := range closers {
			_ = c.Close()
		}
		s.wg.Wait()
		for _, release := range releases {
			release()
		}
		_ = os.RemoveAll(s.directory)
	})
	return err
}

func (s *Service) accept(ctx context.Context) {
	defer s.wg.Done()
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			return
		}
		if s.accepted != nil {
			s.accepted()
		}
		admitted, closed := s.admitConn(conn)
		if closed {
			_ = conn.Close()
			return
		}
		if !admitted {
			s.wg.Add(1)
			go func(c *net.UnixConn) {
				defer s.wg.Done()
				defer s.untrackConn(c, false)
				_ = writeReply(c, ReplyServiceBusy)
				_ = c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
				var discard [headerBytes + MaxHostBytes]byte
				_, _ = c.Read(discard[:])
				_ = c.Close()
			}(conn)
			continue
		}
		s.wg.Add(1)
		go func(c *net.UnixConn) {
			defer s.wg.Done()
			defer s.untrackConn(c, true)
			defer func() { _ = c.Close() }()
			s.handle(ctx, c)
		}(conn)
	}
}

func (s *Service) admitConn(conn *net.UnixConn) (admitted, closed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, true
	}
	s.conns[conn] = struct{}{}
	if s.active >= MaxActiveConnections {
		return false, false
	}
	s.active++
	return true, false
}

func (s *Service) untrackConn(c io.Closer, releaseSlot bool) {
	s.mu.Lock()
	delete(s.conns, c)
	if releaseSlot {
		s.active--
	}
	s.mu.Unlock()
}

func (s *Service) trackUpstream(c io.Closer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *Service) handle(ctx context.Context, conn *net.UnixConn) {
	first, err := s.binding.Verify(ctx, conn, time.Now().Add(s.deadline))
	if err != nil {
		return
	}
	reader := &prefixedReader{prefix: first, r: conn}
	ep, status := decodeConnectRequest(reader)
	switch status {
	case frameOK:
	case frameProtocolAbort:
		return
	case frameInvalidRequest:
		s.recordAttempt(Attempt{
			Stage:   StageRequest,
			Outcome: OutcomeInvalidRequest,
			Reply:   ReplyInvalidRequest,
		})
		_ = writeReply(conn, ReplyInvalidRequest)
		return
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return
	}
	dest := ep.DialAddr()
	dial := s.currentDial()
	if dial == nil {
		s.recordAttempt(Attempt{
			Destination:  dest,
			Host:         ep.Host,
			Port:         ep.Port,
			Stage:        StageBrokerSetup,
			Outcome:      OutcomeSetupFailed,
			Reply:        ReplyDialFailed,
			PolicyReason: "service_unbound",
		})
		_ = writeReply(conn, ReplyDialFailed)
		return
	}
	res, rest, clientClosed := s.dialWhileConnected(ctx, conn, dial, reader.remaining(), ep)
	if clientClosed {
		if res.Conn != nil {
			_ = res.Conn.Close()
		}
		return
	}
	code := res.Reply
	if code != ReplySuccess || res.Conn == nil {
		if res.Conn != nil {
			_ = res.Conn.Close()
		}
		if !code.Valid() || code == ReplySuccess {
			code = ReplyDialFailed
		}
		s.recordAttempt(Attempt{
			Destination:  dest,
			Host:         ep.Host,
			Port:         ep.Port,
			Stage:        defaultStage(res.Stage, code),
			Outcome:      defaultOutcome(res.Outcome, code),
			Reply:        code,
			PolicyReason: res.PolicyReason,
			ErrorClass:   res.ErrorClass,
		})
		_ = writeReply(conn, code)
		return
	}
	if !s.trackUpstream(res.Conn) {
		_ = res.Conn.Close()
		return
	}
	defer func() {
		s.untrackConn(res.Conn, false)
		_ = res.Conn.Close()
	}()
	attemptIdx := s.recordAttempt(Attempt{
		Destination: dest,
		Host:        ep.Host,
		Port:        ep.Port,
		Stage:       defaultStage(res.Stage, ReplySuccess),
		Outcome:     OutcomeEstablished,
		Reply:       ReplySuccess,
	})
	if err := writeReply(conn, ReplySuccess); err != nil {
		return
	}
	obs := &handshakeObserver{}
	relayStreams(ctx, conn, rest, res.Conn, obs)
	sawBanner, reachedAuth := obs.summary()
	s.updateAttemptHandshake(attemptIdx, sawBanner, reachedAuth)
}

func defaultStage(stage string, code ReplyCode) string {
	if stage != "" {
		return stage
	}
	switch code {
	case ReplyLeaseEnded:
		return StageLease
	case ReplyEndpointDenied:
		return StagePolicy
	case ReplyInvalidRequest:
		return StageRequest
	default:
		return StageDial
	}
}

func defaultOutcome(outcome string, code ReplyCode) string {
	if outcome != "" {
		return outcome
	}
	switch code {
	case ReplySuccess:
		return OutcomeEstablished
	case ReplyLeaseEnded:
		return OutcomeLeaseEnded
	case ReplyEndpointDenied:
		return OutcomeEndpointDenied
	case ReplyInvalidRequest:
		return OutcomeInvalidRequest
	default:
		return OutcomeDialFailed
	}
}

func (s *Service) dialWhileConnected(ctx context.Context, conn *net.UnixConn, dial DialFunc, buffered []byte, ep egressproxy.Endpoint) (DialResult, []byte, bool) {
	dialCtx, cancelDial := context.WithCancel(ctx)
	defer cancelDial()

	var probe [1]byte
	var probeN int
	var probeErr error
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		probeN, probeErr = conn.Read(probe[:])
		if probeN == 0 && probeErr != nil && !os.IsTimeout(probeErr) {
			cancelDial()
		}
	}()

	res, err := dial(dialCtx, ep)
	_ = conn.SetReadDeadline(time.Now())
	<-probeDone
	_ = conn.SetReadDeadline(time.Time{})

	if probeN > 0 {
		buffered = append(buffered, probe[:probeN]...)
	} else if probeErr != nil && !os.IsTimeout(probeErr) {
		return res, nil, true
	}
	if err != nil && res.Reply == ReplySuccess {
		res.Reply = ReplyDialFailed
	}
	return res, buffered, false
}

func writeReply(conn *net.UnixConn, code ReplyCode) error {
	if err := conn.SetWriteDeadline(time.Now().Add(replyWriteDeadline)); err != nil {
		return err
	}
	_, err := conn.Write([]byte{byte(code)})
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

func relayStreams(ctx context.Context, client *net.UnixConn, initial []byte, upstream net.Conn, obs *handshakeObserver) {
	touch := func() {
		deadline := time.Now().Add(relayIdleTimeout)
		_ = client.SetDeadline(deadline)
		_ = upstream.SetDeadline(deadline)
	}
	touch()
	stop := context.AfterFunc(ctx, func() {
		_ = client.Close()
		_ = upstream.Close()
	})
	defer stop()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if len(initial) > 0 {
			touch()
			if obs != nil {
				obs.observeClient(initial)
			}
			if _, err := upstream.Write(initial); err != nil {
				if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
					_ = cw.CloseWrite()
				}
				return
			}
		}
		var onClient func([]byte)
		if obs != nil {
			onClient = obs.observeClient
		}
		_, _ = io.Copy(touchWriter{w: upstream, touch: touch}, touchReader{r: client, touch: touch, observe: onClient})
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		var onServer func([]byte)
		if obs != nil {
			onServer = obs.observeServer
		}
		_, _ = io.Copy(touchWriter{w: client, touch: touch}, touchReader{r: upstream, touch: touch, observe: onServer})
		_ = client.CloseWrite()
	}()
	wg.Wait()
}

type prefixedReader struct {
	prefix []byte
	offset int
	r      io.Reader
}

func (p *prefixedReader) Read(b []byte) (int, error) {
	if p.offset < len(p.prefix) {
		n := copy(b, p.prefix[p.offset:])
		p.offset += n
		return n, nil
	}
	return p.r.Read(b)
}

func (p *prefixedReader) remaining() []byte {
	if p.offset >= len(p.prefix) {
		return nil
	}
	return append([]byte(nil), p.prefix[p.offset:]...)
}

type touchReader struct {
	r       io.Reader
	touch   func()
	observe func([]byte)
}

func (t touchReader) Read(p []byte) (int, error) {
	t.touch()
	n, err := t.r.Read(p)
	if n > 0 {
		t.touch()
		if t.observe != nil {
			t.observe(p[:n])
		}
	}
	return n, err
}

type touchWriter struct {
	w     io.Writer
	touch func()
}

func (t touchWriter) Write(p []byte) (int, error) {
	t.touch()
	n, err := t.w.Write(p)
	if n > 0 {
		t.touch()
	}
	return n, err
}
