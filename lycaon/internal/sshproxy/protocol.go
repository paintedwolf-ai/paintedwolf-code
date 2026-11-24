package sshproxy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/githelper"
)

// SocketEnv carries the per-invocation managed SSH service socket to Git's helper.
const SocketEnv = "LYCAON_GIT_SSH_SOCKET"

const (
	// ProtocolVersion identifies the managed Git SSH connect frame format.
	ProtocolVersion byte = 1
	// OpcodeConnect requests an outbound TCP stream through the host broker.
	OpcodeConnect byte = 1
	// MaxHostBytes caps the encoded hostname before allocation.
	MaxHostBytes = 255
	headerBytes  = 6
)

// ReplyCode is the one-byte connect response sent before any SSH bytes flow.
type ReplyCode byte

const (
	ReplySuccess        ReplyCode = 0
	ReplyLeaseEnded     ReplyCode = 1
	ReplyEndpointDenied ReplyCode = 2
	ReplyDialFailed     ReplyCode = 3
	ReplyInvalidRequest ReplyCode = 4
	ReplyServiceBusy    ReplyCode = 5
)

// Valid reports whether r belongs to the closed response set.
func (r ReplyCode) Valid() bool {
	return r <= ReplyServiceBusy
}

func (r ReplyCode) err() error {
	switch r {
	case ReplySuccess:
		return nil
	case ReplyLeaseEnded:
		return errors.New("network authority ended before SSH connect completed")
	case ReplyEndpointDenied:
		return errors.New("egress to SSH host is not allowed")
	case ReplyDialFailed:
		return errors.New("upstream SSH dial failed")
	case ReplyInvalidRequest:
		return errors.New("invalid SSH connect request")
	case ReplyServiceBusy:
		return errors.New("managed SSH service is busy")
	default:
		return fmt.Errorf("unrecognized SSH connect response %d", r)
	}
}

// EncodeConnectRequest formats a version 1 connect frame.
func EncodeConnectRequest(host string, port uint16) ([]byte, error) {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	hostLen := len(host)
	if hostLen <= 0 || hostLen > MaxHostBytes {
		return nil, errors.New("invalid host")
	}
	if port == 0 {
		return nil, errors.New("invalid port")
	}
	frame := make([]byte, headerBytes+hostLen)
	frame[0] = ProtocolVersion
	frame[1] = OpcodeConnect
	binary.BigEndian.PutUint16(frame[2:4], uint16(hostLen))
	binary.BigEndian.PutUint16(frame[4:6], port)
	copy(frame[headerBytes:], host)
	return frame, nil
}

type frameStatus int

const (
	frameOK frameStatus = iota
	frameProtocolAbort
	frameInvalidRequest
)

func decodeConnectRequest(r io.Reader) (egressproxy.Endpoint, frameStatus) {
	var hdr [headerBytes]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return egressproxy.Endpoint{}, frameProtocolAbort
	}
	if hdr[0] != ProtocolVersion {
		return egressproxy.Endpoint{}, frameProtocolAbort
	}
	if hdr[1] != OpcodeConnect {
		return egressproxy.Endpoint{}, frameInvalidRequest
	}
	hostLen := int(binary.BigEndian.Uint16(hdr[2:4]))
	if hostLen <= 0 || hostLen > MaxHostBytes {
		return egressproxy.Endpoint{}, frameInvalidRequest
	}
	port := binary.BigEndian.Uint16(hdr[4:6])
	hostBuf := make([]byte, hostLen)
	if _, err := io.ReadFull(r, hostBuf); err != nil {
		return egressproxy.Endpoint{}, frameProtocolAbort
	}
	if port == 0 {
		return egressproxy.Endpoint{}, frameInvalidRequest
	}
	ep, err := egressproxy.ParseTCPEndpoint(string(hostBuf), port)
	if err != nil {
		return egressproxy.Endpoint{}, frameInvalidRequest
	}
	return ep, frameOK
}

// Connect opens an authenticated managed SSH stream to host:port over socketPath.
func Connect(ctx context.Context, socketPath, host, port string) (*net.UnixConn, ReplyCode, error) {
	host = strings.TrimSpace(host)
	port = strings.TrimSpace(port)
	if host == "" || port == "" {
		return nil, ReplyInvalidRequest, errors.New("ssh-proxy-command requires host and port")
	}
	if strings.ContainsAny(host, "\x00\r\n\t") {
		return nil, ReplyInvalidRequest, errors.New("invalid host")
	}
	portNum, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNum == 0 {
		return nil, ReplyInvalidRequest, errors.New("invalid port")
	}
	frame, err := EncodeConnectRequest(host, uint16(portNum))
	if err != nil {
		return nil, ReplyInvalidRequest, err
	}
	conn, err := githelper.Dial(socketPath)
	if err != nil {
		return nil, 0, fmt.Errorf("connect to managed ssh service: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	if err := conn.SetWriteDeadline(time.Now().Add(githelper.SetupDeadline)); err != nil {
		stop()
		_ = conn.Close()
		return nil, 0, err
	}
	var reply [1]byte
	if err := githelper.WriteIdentified(conn, frame); err != nil {
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if _, rerr := io.ReadFull(conn, reply[:]); rerr == nil {
			stop()
			_ = conn.Close()
			code := ReplyCode(reply[0])
			if code.Valid() && code != ReplySuccess {
				return nil, code, code.err()
			}
		}
		stop()
		_ = conn.Close()
		return nil, 0, err
	}
	if err := conn.SetWriteDeadline(time.Time{}); err != nil {
		stop()
		_ = conn.Close()
		return nil, 0, err
	}
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		stop()
		_ = conn.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, ctxErr
		}
		return nil, 0, fmt.Errorf("read managed ssh reply: %w", err)
	}
	stop()
	code := ReplyCode(reply[0])
	if !code.Valid() || code != ReplySuccess {
		_ = conn.Close()
		return nil, code, code.err()
	}
	return conn, ReplySuccess, nil
}
