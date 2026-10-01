package egressproxy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	socksVersion     = 0x05
	socksAuthNone    = 0x00
	socksCmdConnect  = 0x01
	socksRepSuccess  = 0x00
	socksRepFail     = 0x01
	socksRepHostUn   = 0x04
	socksRepNotAllow = 0x02
	socksRepCmdUn    = 0x07
	socksRepAddrUn   = 0x08

	maxSocksDomainLen = 255
	maxSocksPort      = 1<<16 - 1
)

// serveSOCKS accepts SOCKS5 CONNECT requests on ln until ctx is canceled or ln closes.
func (b *Broker) serveSOCKS(ctx context.Context, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		b.connWG.Add(1)
		go func() {
			defer b.connWG.Done()
			b.handleSOCKSConn(ctx, conn)
		}()
	}
}

func (b *Broker) handleSOCKSConn(parent context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	stopOnClose := context.AfterFunc(parent, func() { _ = conn.Close() })
	defer stopOnClose()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	// Identity comes from the caller, not from anything it can send.
	peer, refusal := b.identify(conn)

	if err := socksHandshake(conn); err != nil {
		return
	}

	ep, rep, err := readSocksConnectRequest(conn)
	if err != nil {
		if rep == 0 {
			rep = socksRepAddrUn
		}
		_ = writeSocksReply(conn, rep, nil)
		return
	}
	ep.Inherited = peer.Inherited
	_ = conn.SetDeadline(time.Time{})

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	if refusal == "" && !b.stillLeased(peer.Lineage) {
		refusal = RefusedLeaseEnded
	}
	if refusal != "" {
		b.noteRefusal(peer, ep, refusal)
		_ = writeSocksReply(conn, socksRepNotAllow, nil)
		return
	}
	if !b.allowedEndpoint(ctx, peer.Lineage, ep) {
		_ = writeSocksReply(conn, socksRepNotAllow, nil)
		return
	}
	if !b.stillLeased(peer.Lineage) {
		b.noteRefusal(peer, ep, RefusedLeaseEnded)
		_ = writeSocksReply(conn, socksRepNotAllow, nil)
		return
	}

	upstream, err := b.dialAuthorizedEndpoint(ctx, peer.Lineage, ep)
	if err != nil {
		rep := byte(socksRepHostUn)
		// Authorization errors use the SOCKS policy-denial reply.
		if errors.Is(err, errCommandAuthorizationRevoked) || errors.Is(err, ErrLoopbackNotAuthorized) {
			rep = socksRepNotAllow
		}
		_ = writeSocksReply(conn, rep, nil)
		return
	}
	defer func() { _ = upstream.Close() }()

	if err := writeSocksReply(conn, socksRepSuccess, upstream.LocalAddr()); err != nil {
		return
	}
	relayHalfClose(ctx, conn, upstream)
}

func (b *Broker) allowedEndpoint(ctx context.Context, lineage string, ep Endpoint) bool {
	if b.decideEndpoint == nil {
		return true
	}
	return b.decideEndpoint(ctx, lineage, ep)
}

func socksHandshake(conn net.Conn) error {
	var hdr [2]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return err
	}
	if hdr[0] != socksVersion {
		return fmt.Errorf("unsupported version")
	}
	nmethods := int(hdr[1])
	if nmethods <= 0 || nmethods > 8 {
		return fmt.Errorf("invalid methods")
	}
	methods := make([]byte, nmethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	for _, m := range methods {
		if m == socksAuthNone {
			_, err := conn.Write([]byte{socksVersion, socksAuthNone})
			return err
		}
	}
	_, _ = conn.Write([]byte{socksVersion, 0xff})
	return fmt.Errorf("no acceptable auth")
}

func readSocksConnectRequest(conn net.Conn) (Endpoint, byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return Endpoint{}, socksRepFail, err
	}
	if hdr[0] != socksVersion {
		return Endpoint{}, socksRepFail, fmt.Errorf("bad version")
	}
	if hdr[2] != 0x00 {
		return Endpoint{}, socksRepFail, fmt.Errorf("bad reserved byte")
	}
	if hdr[1] != socksCmdConnect {
		// Drain address+port so clients can read a clean reply.
		atyp := hdr[3]
		_, _ = readSocksAddress(conn, atyp)
		var portBuf [2]byte
		_, _ = io.ReadFull(conn, portBuf[:])
		return Endpoint{}, socksRepCmdUn, fmt.Errorf("unsupported command %d", hdr[1])
	}
	atyp := hdr[3]
	addr, err := readSocksAddress(conn, atyp)
	if err != nil {
		return Endpoint{}, socksRepAddrUn, err
	}
	var portBuf [2]byte
	if _, err := io.ReadFull(conn, portBuf[:]); err != nil {
		return Endpoint{}, socksRepFail, err
	}
	port := binary.BigEndian.Uint16(portBuf[:])
	ep, err := ParseSocksEndpoint(atyp, addr, port)
	if err != nil {
		return Endpoint{}, socksRepAddrUn, err
	}
	return ep, socksRepSuccess, nil
}

func readSocksAddress(conn net.Conn, atyp byte) ([]byte, error) {
	switch atyp {
	case 0x01:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return nil, err
		}
		return buf, nil
	case 0x04:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return nil, err
		}
		return buf, nil
	case 0x03:
		var lenBuf [1]byte
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			return nil, err
		}
		n := int(lenBuf[0])
		if n == 0 || n > maxSocksDomainLen {
			return nil, fmt.Errorf("bad domain len")
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return nil, err
		}
		return buf, nil
	default:
		return nil, fmt.Errorf("bad atyp")
	}
}
func writeSocksReply(conn net.Conn, rep byte, bound net.Addr) error {
	buf := []byte{socksVersion, rep, 0x00}
	tcpAddr, ok := bound.(*net.TCPAddr)
	if !ok || tcpAddr == nil {
		buf = append(buf, 0x01, 0, 0, 0, 0, 0, 0)
		_, err := conn.Write(buf)
		return err
	}
	if ip4 := tcpAddr.IP.To4(); ip4 != nil {
		buf = append(buf, 0x01)
		buf = append(buf, ip4...)
	} else if ip6 := tcpAddr.IP.To16(); ip6 != nil {
		buf = append(buf, 0x04)
		buf = append(buf, ip6...)
	} else {
		buf = append(buf, 0x01, 0, 0, 0, 0)
	}
	if tcpAddr.Port < 0 || tcpAddr.Port > maxSocksPort {
		return fmt.Errorf("SOCKS bound port out of range: %d", tcpAddr.Port)
	}
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], uint16(tcpAddr.Port))
	buf = append(buf, port[:]...)
	_, err := conn.Write(buf)
	return err
}
