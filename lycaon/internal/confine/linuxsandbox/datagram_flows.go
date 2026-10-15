package linuxsandbox

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// datagramFlowIdle ends a flow that has carried nothing for this long.
	datagramFlowIdle = 60 * time.Second
	// maxDatagramFlows bounds the flows one relay or gateway keeps open;
	// datagrams that would open another are dropped.
	maxDatagramFlows = 1024
	maxDatagram      = 65535
)

// datagramFlows carries datagrams for many peers, each over a connected
// socket of its own, and delivers what comes back on that socket to the peer.
type datagramFlows struct {
	mu     sync.Mutex
	flows  map[any]*datagramFlow
	closed bool
}

type datagramFlow struct {
	conn net.Conn
	last atomic.Int64
}

func newDatagramFlows() *datagramFlows {
	return &datagramFlows{flows: map[any]*datagramFlow{}}
}

// flowPeer is where a flow's replies go: deliver takes each datagram, and
// refused, when set, hears that the far end reported its port closed.
type flowPeer struct {
	deliver func([]byte)
	refused func()
}

// send writes payload on key's flow, opening it with dial and returning its
// replies to peer when it is new.
func (t *datagramFlows) send(key any, payload []byte, dial func() (net.Conn, error), peer flowPeer) {
	f := t.flow(key, dial, peer)
	if f == nil {
		return
	}
	f.last.Store(time.Now().UnixNano())
	_, _ = f.conn.Write(payload)
}

func (t *datagramFlows) flow(key any, dial func() (net.Conn, error), peer flowPeer) *datagramFlow {
	t.mu.Lock()
	defer t.mu.Unlock()
	if f, ok := t.flows[key]; ok {
		return f
	}
	if t.closed || len(t.flows) >= maxDatagramFlows {
		return nil
	}
	conn, err := dial()
	if err != nil {
		return nil
	}
	f := &datagramFlow{conn: conn}
	t.flows[key] = f
	go t.replies(key, f, peer)
	return f
}

// replies delivers what the flow's socket receives until the flow idles out
// or the table closes.
func (t *datagramFlows) replies(key any, f *datagramFlow, peer flowPeer) {
	defer t.drop(key, f)
	buf := make([]byte, maxDatagram)
	for {
		_ = f.conn.SetReadDeadline(time.Now().Add(datagramFlowIdle))
		n, err := f.conn.Read(buf)
		if err == nil {
			f.last.Store(time.Now().UnixNano())
			peer.deliver(buf[:n])
			continue
		}
		var ne net.Error
		switch {
		case errors.As(err, &ne) && ne.Timeout():
			if time.Since(time.Unix(0, f.last.Load())) >= datagramFlowIdle {
				return
			}
		case errors.Is(err, syscall.ECONNREFUSED):
			// The far end refused one datagram; the next may be served.
			if peer.refused != nil {
				peer.refused()
			}
		default:
			return
		}
	}
}

func (t *datagramFlows) drop(key any, f *datagramFlow) {
	t.mu.Lock()
	if t.flows[key] == f {
		delete(t.flows, key)
	}
	t.mu.Unlock()
	_ = f.conn.Close()
}

// Close ends every flow and opens no more.
func (t *datagramFlows) Close() error {
	t.mu.Lock()
	t.closed = true
	flows := t.flows
	t.flows = map[any]*datagramFlow{}
	t.mu.Unlock()
	for _, f := range flows {
		_ = f.conn.Close()
	}
	return nil
}

// relayDatagrams carries what listen receives, per sender, over a flow dial
// opens, and returns each flow's replies to its sender from listen's address.
func relayDatagrams(listen net.PacketConn, dial func() (net.Conn, error)) {
	flows := newDatagramFlows()
	defer func() { _ = flows.Close() }()
	buf := make([]byte, maxDatagram)
	for {
		n, from, err := listen.ReadFrom(buf)
		if err != nil {
			return
		}
		key := from.String()
		flows.send(key, buf[:n], dial, flowPeer{deliver: func(reply []byte) { _, _ = listen.WriteTo(reply, from) }})
	}
}
