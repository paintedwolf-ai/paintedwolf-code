package outboundhttp

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// wireHeaderLimit bounds the captured header bytes per hop.
const wireHeaderLimit = 64 << 10

// wireCapture records the leading response bytes a hop's connection reads,
// up to the end of the first non-informational header block.
type wireCapture struct {
	mu   sync.Mutex
	data []byte
	done bool
}

func captureWire(transport *http.Transport) *wireCapture {
	capture := &wireCapture{}
	dial := transport.DialContext
	if dial == nil {
		var d net.Dialer
		dial = d.DialContext
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &wireConn{Conn: conn, capture: capture}, nil
	}
	return capture
}

type wireConn struct {
	net.Conn
	capture *wireCapture
}

func (c *wireConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.capture.record(p[:n])
	return n, err
}

func (c *wireCapture) record(b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done || len(b) == 0 {
		return
	}
	room := wireHeaderLimit - len(c.data)
	if room <= 0 {
		c.done = true
		return
	}
	c.data = append(c.data, b[:min(len(b), room)]...)
	if c.finalBlock() >= 0 {
		c.done = true
	}
}

// finalBlock is the end of the first header block whose status is not 1xx,
// or -1 while it is incomplete.
func (c *wireCapture) finalBlock() int {
	offset := 0
	for {
		end := bytes.Index(c.data[offset:], []byte("\r\n\r\n"))
		if end < 0 {
			return -1
		}
		block := c.data[offset : offset+end]
		offset += end + 4
		if status, ok := blockStatus(block); ok && (status < 100 || status >= 200) {
			return offset
		}
	}
}

func blockStatus(block []byte) (int, bool) {
	line, _, _ := bytes.Cut(block, []byte("\r\n"))
	fields := strings.Fields(string(line))
	if len(fields) < 2 {
		return 0, false
	}
	status, err := strconv.Atoi(fields[1])
	return status, err == nil
}

// headers parses the captured final block when its status matches the
// response the transport returned; otherwise nothing is claimed.
func (c *wireCapture) headers(status int) *WireHeaders {
	c.mu.Lock()
	defer c.mu.Unlock()
	end := c.finalBlock()
	if end < 0 {
		return nil
	}
	data := c.data[:end-4]
	start := bytes.LastIndex(data, []byte("\r\n\r\n"))
	if start >= 0 {
		data = data[start+4:]
	}
	lines := strings.Split(string(data), "\r\n")
	if got, ok := blockStatus([]byte(lines[0])); !ok || got != status {
		return nil
	}
	out := &WireHeaders{StatusLine: lines[0], Raw: string(data) + "\r\n\r\n"}
	for _, line := range lines[1:] {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil
		}

		out.Headers = append(out.Headers, Header{Name: name, Value: strings.TrimLeft(value, " \t")})
	}
	return out
}
