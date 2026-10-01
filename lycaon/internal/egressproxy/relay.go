package egressproxy

import (
	"context"
	"io"
	"net"
	"sync"
	"time"
)

const relayIdleTimeout = 5 * time.Minute

type activityConn struct {
	net.Conn
	touch func()
}

func (c activityConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (c activityConn) Read(p []byte) (int, error) {
	c.touch()
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.touch()
	}
	return n, err
}

func (c activityConn) Write(p []byte) (int, error) {
	c.touch()
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.touch()
	}
	return n, err
}

func relayHalfClose(ctx context.Context, a, b net.Conn) {
	touch := func() {
		deadline := time.Now().Add(relayIdleTimeout)
		_ = a.SetDeadline(deadline)
		_ = b.SetDeadline(deadline)
	}
	touch()
	stop := context.AfterFunc(ctx, func() {
		_ = a.Close()
		_ = b.Close()
	})
	defer stop()
	aActive := activityConn{Conn: a, touch: touch}
	bActive := activityConn{Conn: b, touch: touch}
	var wg sync.WaitGroup
	copyConn := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}
	wg.Add(2)
	go copyConn(bActive, aActive)
	go copyConn(aActive, bActive)
	wg.Wait()
}
