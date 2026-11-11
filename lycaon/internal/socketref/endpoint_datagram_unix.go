//go:build darwin || linux

package socketref

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const endpointDatagramBytes = 64 << 10

type datagramPeer struct {
	conn   *net.UnixConn
	local  string
	client *net.UnixAddr
}

// Datagram associations have bounded membership and no queued payloads.
type datagramEndpoint struct {
	conn     *net.UnixConn
	dial     func(context.Context, string) (net.Conn, error)
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	peers    map[string]*datagramPeer
	closed   bool
	sequence uint64
	wg       sync.WaitGroup
	once     sync.Once
	remove   func() error
	closeErr error
}

func startDatagramEndpoint(path string, dial func(context.Context, string) (net.Conn, error)) (*datagramEndpoint, error) {
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Net: "unixgram", Name: path})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &datagramEndpoint{conn: conn, dial: dial, ctx: ctx, cancel: cancel, peers: make(map[string]*datagramPeer)}
	e.wg.Add(1)
	go e.receive()
	return e, nil
}

func (e *datagramEndpoint) Path() string { return e.conn.LocalAddr().String() }

func (e *datagramEndpoint) Close() error {
	e.once.Do(func() {
		e.mu.Lock()
		e.closed = true
		e.cancel()
		_ = e.conn.Close()
		for _, peer := range e.peers {
			_ = peer.conn.Close()
		}
		e.mu.Unlock()
		e.wg.Wait()
		if e.remove != nil {
			e.closeErr = e.remove()
		} else {
			e.closeErr = os.Remove(e.Path())
		}
	})
	return e.closeErr
}

func (e *datagramEndpoint) receive() {
	defer e.wg.Done()
	data, control := make([]byte, endpointDatagramBytes), make([]byte, unix.CmsgSpace(4*endpointMessageFDs))
	for {
		n, oobn, fds, sender, err := endpointDescriptors.read(e.conn, data, control)
		if err == nil {
			key := ""
			if sender != nil {
				key = sender.Name
			}
			peer, peerErr := e.peer(key)
			if peerErr == nil {
				_ = peer.conn.SetReadDeadline(time.Now().Add(time.Minute))
				peerErr = writeUnixDatagram(peer.conn, data[:n], control[:oobn], nil)
			}
			if peerErr != nil && peer != nil {
				_ = peer.conn.Close()
			}
		}
		endpointDescriptors.release(fds)
		if err != nil && errors.Is(err, net.ErrClosed) {
			return
		}
		if e.ctx.Err() != nil {
			return
		}
	}
}

func (e *datagramEndpoint) peer(key string) (*datagramPeer, error) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, net.ErrClosed
	}
	if peer := e.peers[key]; peer != nil {
		e.mu.Unlock()
		return peer, nil
	}
	if len(e.peers) >= endpointConnections {
		e.mu.Unlock()
		return nil, fmt.Errorf("local datagram association limit reached")
	}
	e.sequence++
	local := filepath.Join(filepath.Dir(e.Path()), fmt.Sprintf("peer-%d", e.sequence))
	e.mu.Unlock()
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	upstream, err := e.dial(ctx, local)
	cancel()
	if err != nil {
		_ = os.Remove(local)
		return nil, err
	}
	conn, ok := upstream.(*net.UnixConn)
	if !ok {
		_ = upstream.Close()
		_ = os.Remove(local)
		return nil, ErrUnsupported
	}
	peer := &datagramPeer{conn: conn, local: local}
	if key != "" {
		peer.client = &net.UnixAddr{Net: "unixgram", Name: key}
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		_ = conn.Close()
		_ = os.Remove(local)
		return nil, net.ErrClosed
	}
	e.peers[key] = peer
	e.wg.Add(1)
	e.mu.Unlock()
	go e.respond(key, peer)
	return peer, nil
}

func (e *datagramEndpoint) respond(key string, peer *datagramPeer) {
	defer e.wg.Done()
	defer func() {
		_ = peer.conn.Close()
		_ = os.Remove(peer.local)
		e.mu.Lock()
		delete(e.peers, key)
		e.mu.Unlock()
	}()
	data, control := make([]byte, endpointDatagramBytes), make([]byte, unix.CmsgSpace(4*endpointMessageFDs))
	for {
		_ = peer.conn.SetReadDeadline(time.Now().Add(time.Minute))
		n, oobn, fds, _, err := endpointDescriptors.read(peer.conn, data, control)
		if err == nil && peer.client != nil {
			err = writeUnixDatagram(e.conn, data[:n], control[:oobn], peer.client)
		}
		endpointDescriptors.release(fds)
		if err != nil {
			return
		}
	}
}
