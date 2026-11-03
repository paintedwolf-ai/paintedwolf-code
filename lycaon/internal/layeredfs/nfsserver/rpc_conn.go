package nfsserver

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"

	nfs "github.com/willscott/go-nfs"
)

type connState struct {
	mu            sync.Mutex
	boundViewHash uint64
}

func (cs *connState) getBoundView() uint64 {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.boundViewHash
}

func (cs *connState) bindView(vHash uint64) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.boundViewHash != 0 && cs.boundViewHash != vHash {
		return false
	}
	cs.boundViewHash = vHash
	return true
}

func (cs *connState) clearView(vHash uint64) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.boundViewHash == vHash {
		cs.boundViewHash = 0
	}
}

type rpcCallHeader struct {
	XID     uint32
	MsgType uint32
	RPCVers uint32
	Prog    uint32
	Vers    uint32
	Proc    uint32
	Cred    authCred
	Body    *bytes.Reader
}

func serveConn(s *Server, conn net.Conn) {
	var writeMu sync.Mutex
	cs := &connState{}
	defer releaseConnState(s, cs)

	for {
		payload, err := readRPCMessage(conn, s.cfg.MaxRequestBytes)
		if err != nil || s.state.closed.Load() {
			return
		}

		done := make(chan []byte, 1)
		task := func() {
			done <- handleRPCPayload(s, cs, payload)
		}
		select {
		case s.queue <- task:
		default:
			reply := handleOverloadedPayload(payload)
			if len(reply) > 0 {
				writeMu.Lock()
				_ = writeRPCFrame(conn, reply)
				writeMu.Unlock()
			}
			continue
		}

		reply := <-done
		if len(reply) > 0 {
			writeMu.Lock()
			err = writeRPCFrame(conn, reply)
			writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// readRPCMessage reads a record-marked RPC message while enforcing maxBytes before allocating.
func readRPCMessage(r io.Reader, maxBytes int) ([]byte, error) {
	var total []byte
	for {
		var hdrBuf [4]byte
		if _, err := io.ReadFull(r, hdrBuf[:]); err != nil {
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
			if _, err := io.ReadFull(r, frag); err != nil {
				return nil, err
			}
			if len(total) == 0 && isLast {
				return frag, nil
			}
			total = append(total, frag...)
		}
		if isLast {
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
	if cred.UID, err = readUint32(br); err != nil {
		return authCred{}, err
	}
	if cred.GID, err = readUint32(br); err != nil {
		return authCred{}, err
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
	writeUint32(&out, xid)
	writeUint32(&out, 1) // REPLY
	writeUint32(&out, 0) // MSG_ACCEPTED
	writeUint32(&out, 0) // AUTH_NULL verifier flavor
	writeUint32(&out, 0) // AUTH_NULL verifier length
	writeUint32(&out, uint32(acceptStat))
	if len(body) > 0 {
		_, _ = out.Write(body)
	}
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
	writeUint32(&out, xid)
	writeUint32(&out, 1) // REPLY
	writeUint32(&out, 1) // MSG_DENIED
	writeUint32(&out, 0) // RPC_MISMATCH
	writeUint32(&out, low)
	writeUint32(&out, high)
	return out.Bytes()
}

func releaseConnState(s *Server, cs *connState) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	for _, entries := range s.state.exportsByVH {
		for _, e := range entries {
			if e.activeConn == cs {
				e.activeConn = nil
			}
		}
	}
}

func resolveAndAuthHandle(s *Server, cs *connState, cred authCred, raw []byte) (*handleRecord, *exportEntry, error) {
	if cred.Flavor != nfs.AuthFlavorUnix {
		return nil, nil, ErrUnauthorizedRPC
	}
	rec, err := s.handles.Resolve(raw, cs.getBoundView())
	if err != nil {
		return nil, nil, err
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	for _, e := range s.state.exportsByVH[rec.key.viewHash] {
		if !e.mounted || e.cap.AllowedUID != cred.UID {
			continue
		}
		if e.activeConn != nil && e.activeConn != cs {
			continue
		}
		if !cs.bindView(rec.key.viewHash) {
			return nil, nil, ErrCrossViewHandle
		}
		e.activeConn = cs
		return rec, e, nil
	}
	return nil, nil, ErrUnauthorizedRPC
}
