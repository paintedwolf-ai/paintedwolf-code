//go:build darwin || linux

package socketref

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

const endpointConnections = 16

// unixEndpoint owns all accepted sockets and joins every forwarding operation.
type unixEndpoint struct {
	listener    *net.UnixListener
	dial        func(context.Context) (net.Conn, error)
	ctx         context.Context
	cancel      context.CancelFunc
	slots       chan struct{}
	mu          sync.Mutex
	closed      bool
	connections map[*net.UnixConn]struct{}
	wg          sync.WaitGroup
	once        sync.Once
	remove      func() error
	closeErr    error
}

func startEndpoint(path string, dial func(context.Context) (net.Conn, error)) (*unixEndpoint, error) {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: path})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &unixEndpoint{listener: listener, dial: dial, ctx: ctx, cancel: cancel, slots: make(chan struct{}, endpointConnections), connections: make(map[*net.UnixConn]struct{})}
	e.wg.Add(1)
	go e.accept()
	return e, nil
}

func (e *unixEndpoint) Path() string { return e.listener.Addr().String() }

func (e *unixEndpoint) Close() error {
	e.once.Do(func() {
		e.mu.Lock()
		e.closed = true
		e.cancel()
		_ = e.listener.Close()
		for conn := range e.connections {
			_ = conn.Close()
		}
		e.mu.Unlock()
		e.wg.Wait()
		if e.remove != nil {
			e.closeErr = e.remove()
		}
	})
	return e.closeErr
}

func (e *unixEndpoint) track(conn *net.UnixConn) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		_ = conn.Close()
		return false
	}
	e.connections[conn] = struct{}{}
	return true
}

func (e *unixEndpoint) release(conn *net.UnixConn) {
	_ = conn.Close()
	e.mu.Lock()
	delete(e.connections, conn)
	e.mu.Unlock()
}

func (e *unixEndpoint) accept() {
	defer e.wg.Done()
	for {
		conn, err := e.listener.AcceptUnix()
		if err != nil {
			return
		}
		select {
		case e.slots <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		if !e.track(conn) {
			<-e.slots
			return
		}
		e.wg.Add(1)
		go e.forward(conn)
	}
}

func (e *unixEndpoint) forward(client *net.UnixConn) {
	defer e.wg.Done()
	defer func() { <-e.slots }()
	defer e.release(client)
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	upstream, err := e.dial(ctx)
	cancel()
	if err != nil {
		return
	}
	service, ok := upstream.(*net.UnixConn)
	if !ok {
		_ = upstream.Close()
		return
	}
	if !e.track(service) {
		return
	}
	defer e.release(service)
	// dial has attested the destination before either direction can cross.
	finished := make(chan error, 2)
	go func() { finished <- forwardUnixMessages(service, client) }()
	go func() { finished <- forwardUnixMessages(client, service) }()
	if err := <-finished; err != nil && !errors.Is(err, net.ErrClosed) {
		_ = client.Close()
		_ = service.Close()
	}
	<-finished
}
