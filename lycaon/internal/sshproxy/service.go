package sshproxy

import (
	"context"
	"io"
	"net"
	"os"
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

// DialFunc dials an authorized endpoint on behalf of the service's bound action.
type DialFunc func(ctx context.Context, ep egressproxy.Endpoint) (net.Conn, ReplyCode, error)

// ServeOptions configures one per-invocation SSH service.
type ServeOptions struct {
	GitBinary string
	Dial      DialFunc
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
	active           int
	closed           bool
	conns            map[io.Closer]struct{}
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
		cancel:    cancel,
		listener:  listener,
		directory: directory,
		binding:   binding,
		dial:      options.Dial,
		deadline:  githelper.SetupDeadline,
		conns:     make(map[io.Closer]struct{}),
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
		_ = writeReply(conn, ReplyInvalidRequest)
		return
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return
	}
	if s.dial == nil {
		_ = writeReply(conn, ReplyDialFailed)
		return
	}
	upstream, code, rest, clientClosed := s.dialWhileConnected(ctx, conn, reader.remaining(), ep)
	if clientClosed {
		if upstream != nil {
			_ = upstream.Close()
		}
		return
	}
	if code != ReplySuccess || upstream == nil {
		if upstream != nil {
			_ = upstream.Close()
		}
		if !code.Valid() || code == ReplySuccess {
			code = ReplyDialFailed
		}
		_ = writeReply(conn, code)
		return
	}
	if !s.trackUpstream(upstream) {
		_ = upstream.Close()
		return
	}
	defer func() {
		s.untrackConn(upstream, false)
		_ = upstream.Close()
	}()
	if err := writeReply(conn, ReplySuccess); err != nil {
		return
	}
	relayStreams(ctx, conn, rest, upstream)
}

func (s *Service) dialWhileConnected(ctx context.Context, conn *net.UnixConn, buffered []byte, ep egressproxy.Endpoint) (net.Conn, ReplyCode, []byte, bool) {
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

	upstream, code, err := s.dial(dialCtx, ep)
	_ = conn.SetReadDeadline(time.Now())
	<-probeDone
	_ = conn.SetReadDeadline(time.Time{})

	if probeN > 0 {
		buffered = append(buffered, probe[:probeN]...)
	} else if probeErr != nil && !os.IsTimeout(probeErr) {
		return upstream, code, nil, true
	}
	if err != nil && code == ReplySuccess {
		code = ReplyDialFailed
	}
	return upstream, code, buffered, false
}

func writeReply(conn *net.UnixConn, code ReplyCode) error {
	if err := conn.SetWriteDeadline(time.Now().Add(replyWriteDeadline)); err != nil {
		return err
	}
	_, err := conn.Write([]byte{byte(code)})
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

func relayStreams(ctx context.Context, client *net.UnixConn, initial []byte, upstream net.Conn) {
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
			if _, err := upstream.Write(initial); err != nil {
				if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
					_ = cw.CloseWrite()
				}
				return
			}
		}
		_, _ = io.Copy(touchWriter{w: upstream, touch: touch}, touchReader{r: client, touch: touch})
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(touchWriter{w: client, touch: touch}, touchReader{r: upstream, touch: touch})
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
	r     io.Reader
	touch func()
}

func (t touchReader) Read(p []byte) (int, error) {
	t.touch()
	n, err := t.r.Read(p)
	if n > 0 {
		t.touch()
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
