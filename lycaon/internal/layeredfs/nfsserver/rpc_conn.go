package nfsserver

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	nfs "github.com/willscott/go-nfs"
)

type connState struct {
	mu            sync.Mutex
	boundViewHash uint64
	mountDenied   bool
}

func (cs *connState) getBoundView() uint64 {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.boundViewHash
}

func (cs *connState) bindView(vHash uint64) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.mountDenied || (cs.boundViewHash != 0 && cs.boundViewHash != vHash) {
		return false
	}
	cs.boundViewHash = vHash
	return true
}

func (cs *connState) denyMount() {
	cs.mu.Lock()
	cs.mountDenied = true
	cs.mu.Unlock()
}

func (cs *connState) isMountDenied() bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.mountDenied
}

func (cs *connState) clearView(vHash uint64) {
	cs.mu.Lock()
	if vHash == 0 || cs.boundViewHash == vHash {
		cs.boundViewHash = 0
	}
	cs.mu.Unlock()
}

type rpcCallHeader struct {
	XID, MsgType, RPCVers, Prog, Vers, Proc uint32
	Cred                                    authCred
	Body                                    *bytes.Reader
}

// serveConn reads requests and answers each as it finishes, out of order as RPC over TCP allows.
// The kernel client multiplexes a whole mount over one connection, so waiting for one reply before
// reading the next would let one slow COMMIT time out every other operation on the mount.
func serveConn(s *Server, conn net.Conn) {
	var writeMu sync.Mutex
	cs := &connState{}
	reply := func(body []byte) {
		if len(body) == 0 {
			return
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(s.cfg.IdleTimeout))
		if err := writeRPCFrame(conn, body); err != nil {
			_ = conn.Close()
		}
	}
	for {
		payload, err := readRPCMessage(conn, s.cfg.MaxRequestBytes, s.cfg.IdleTimeout, s.cfg.ReadHeaderTimeout)
		if err != nil || s.state.closed.Load() {
			return
		}
		select {
		case <-s.stopped:
			return
		case s.queue <- func() { reply(handleRPCPayload(s, cs, payload)) }:
		default:
			reply(handleOverloadedPayload(payload))
		}
	}
}

// readRPCMessage reads a record-marked RPC message while enforcing maxBytes and per-record deadlines.
func readRPCMessage(conn net.Conn, maxBytes int, idleTimeout, recordTimeout time.Duration) ([]byte, error) {
	var total []byte
	firstFrag := true
	for {
		var hdrBuf [4]byte
		if firstFrag {
			_ = conn.SetReadDeadline(time.Now().Add(idleTimeout))
			if _, err := io.ReadFull(conn, hdrBuf[:1]); err != nil {
				return nil, err
			}
			_ = conn.SetReadDeadline(time.Now().Add(recordTimeout))
			if _, err := io.ReadFull(conn, hdrBuf[1:4]); err != nil {
				return nil, err
			}
			firstFrag = false
		} else if _, err := io.ReadFull(conn, hdrBuf[:]); err != nil {
			return nil, err
		}
		hdr := binary.BigEndian.Uint32(hdrBuf[:])
		isLast := (hdr & 0x80000000) != 0
		fragLen := int(hdr & 0x7fffffff)
		if fragLen < 0 || fragLen > maxBytes || len(total)+fragLen > maxBytes {
			return nil, ErrFrameTooLarge
		}
		if fragLen > 0 {
			frag := make([]byte, fragLen)
			if _, err := io.ReadFull(conn, frag); err != nil {
				return nil, err
			}
			if len(total) == 0 && isLast {
				_ = conn.SetReadDeadline(time.Time{})
				return frag, nil
			}
			total = append(total, frag...)
		}
		if isLast {
			_ = conn.SetReadDeadline(time.Time{})
			return total, nil
		}
	}
}

func writeRPCFrame(w io.Writer, body []byte) error {
	hdr := uint32(len(body)) | 0x80000000
	var frame bytes.Buffer
	frame.Grow(4 + len(body))
	writeUint32(&frame, hdr)
	_, _ = frame.Write(body)
	_, err := w.Write(frame.Bytes())
	return err
}

func parseRPCCall(payload []byte) (rpcCallHeader, error) {
	r := bytes.NewReader(payload)
	var call rpcCallHeader
	var err error
	for _, dst := range []*uint32{&call.XID, &call.MsgType, &call.RPCVers, &call.Prog, &call.Vers, &call.Proc} {
		if *dst, err = readUint32(r); err != nil {
			return call, err
		}
	}
	if call.MsgType != 0 {
		return call, errors.New("not an RPC CALL message")
	}
	if call.Cred, err = readRPCAuth(r); err != nil {
		return call, err
	}
	if _, err := readUint32(r); err != nil {
		return call, err
	}
	if _, err := readOpaqueBounded(r, 400); err != nil {
		return call, err
	}
	call.Body = r
	return call, nil
}

func readRPCAuth(r *bytes.Reader) (authCred, error) {
	flavor, err := readUint32(r)
	if err != nil {
		return authCred{}, err
	}
	body, err := readOpaqueBounded(r, 400)
	if err != nil {
		return authCred{}, err
	}
	cred := authCred{Flavor: nfs.AuthFlavor(flavor)}
	if cred.Flavor != nfs.AuthFlavorUnix {
		return cred, nil
	}
	br := bytes.NewReader(body)
	if _, err := readUint32(br); err != nil {
		return authCred{}, err
	}
	if cred.MachineName, err = readStringBounded(br, 255); err != nil {
		return authCred{}, err
	}
	for _, dst := range []*uint32{&cred.UID, &cred.GID} {
		if *dst, err = readUint32(br); err != nil {
			return authCred{}, err
		}
	}
	numGroups, err := readUint32(br)
	if err != nil || numGroups > 64 {
		return authCred{}, errors.New("invalid AUTH_SYS groups")
	}
	cred.Groups = make([]uint32, numGroups)
	for i := uint32(0); i < numGroups; i++ {
		if cred.Groups[i], err = readUint32(br); err != nil {
			return authCred{}, err
		}
	}
	return cred, nil
}

func handleOverloadedPayload(payload []byte) []byte {
	call, err := parseRPCCall(payload)
	if err != nil {
		return nil
	}
	if call.Prog == nfsProgramID && call.Vers == nfsVersion3 && call.Proc != uint32(nfs.NFSProcedureNull) {
		var body bytes.Buffer
		writeUint32(&body, uint32(nfs.NFSStatusJukebox))
		writeErrorFormattingTail(&body, nfs.NFSProcedure(call.Proc))
		return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
	}
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSystemErr, nil)
}

func handleRPCPayload(s *Server, cs *connState, payload []byte) []byte {
	if len(payload) < 8 {
		return nil
	}
	xid := binary.BigEndian.Uint32(payload[0:4])
	call, err := parseRPCCall(payload)
	if err != nil {
		if len(payload) >= 12 && binary.BigEndian.Uint32(payload[8:12]) == rpcVersion2 {
			return encodeAcceptedReply(xid, nfs.ResponseCodeGarbageArgs, nil)
		}
		return nil
	}
	if call.RPCVers != rpcVersion2 {
		return encodeRPCMismatchReply(call.XID, rpcVersion2, rpcVersion2)
	}
	switch call.Prog {
	case mountProgramID:
		if call.Vers != mountVersion3 {
			return encodeProgMismatchReply(call.XID, mountVersion3, mountVersion3)
		}
		return dispatchMount(s, cs, call)
	case nfsProgramID:
		if call.Vers != nfsVersion3 {
			return encodeProgMismatchReply(call.XID, nfsVersion3, nfsVersion3)
		}
		return dispatchNFS(s, cs, call)
	default:
		return encodeAcceptedReply(call.XID, nfs.ResponseCodeProgUnavailable, nil)
	}
}

func encodeAcceptedReply(xid uint32, acceptStat nfs.ResponseCode, body []byte) []byte {
	var out bytes.Buffer
	out.Grow(24 + len(body))
	for _, v := range []uint32{xid, 1, 0, 0, 0, uint32(acceptStat)} {
		writeUint32(&out, v)
	}
	_, _ = out.Write(body)
	return out.Bytes()
}

func encodeProgMismatchReply(xid, low, high uint32) []byte {
	var body bytes.Buffer
	writeUint32(&body, low)
	writeUint32(&body, high)
	return encodeAcceptedReply(xid, nfs.ResponseCodeProgMismatch, body.Bytes())
}

func encodeRPCMismatchReply(xid, low, high uint32) []byte {
	var out bytes.Buffer
	for _, v := range []uint32{xid, 1, 1, 0, low, high} {
		writeUint32(&out, v)
	}
	return out.Bytes()
}

func resolveAndAuthHandle(s *Server, cs *connState, cred authCred, raw []byte) (*handleRecord, *exportEntry, error) {
	if cred.Flavor != nfs.AuthFlavorUnix || cs.isMountDenied() {
		return nil, nil, ErrUnauthorizedRPC
	}
	rec, err := s.handles.Resolve(raw, cs.getBoundView())
	if err != nil {
		return nil, nil, err
	}
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	for _, e := range s.state.exportsByVH[rec.key.viewHash] {
		if !e.mounted || e.cap.AllowedUID != cred.UID || e.cap.AllowedGID != cred.GID {
			continue
		}
		if !cs.bindView(rec.key.viewHash) {
			return nil, nil, ErrCrossViewHandle
		}
		return rec, e, nil
	}
	return nil, nil, ErrUnauthorizedRPC
}
