package sshproxy

import (
	"bytes"
	"encoding/binary"
	"sync"
)

const (
	sshMsgNewKeys        = 21
	maxBannerInspect     = 1024
	maxKexPacketLen      = 65536
	maxKexPacketsInspect = 12
)

// handshakeObserver passively inspects cleartext RFC 4253 framing prior to encryption
// so the host knows whether an SSH server banner arrived and whether client host-key
// verification completed (marked by unencrypted client SSH_MSG_NEWKEYS).
type handshakeObserver struct {
	mu              sync.Mutex
	serverBuf       []byte
	serverDone      bool
	sawServerBanner bool

	clientBuf       []byte
	clientBannerEnd bool
	clientSkip      int
	clientPackets   int
	clientDone      bool
	reachedUserAuth bool
}

func (o *handshakeObserver) observeServer(p []byte) {
	if len(p) == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.serverDone {
		return
	}
	remain := maxBannerInspect - len(o.serverBuf)
	if remain <= 0 {
		o.serverDone = true
		return
	}
	if len(p) > remain {
		p = p[:remain]
	}
	o.serverBuf = append(o.serverBuf, p...)
	if hasSSHBannerLine(o.serverBuf) {
		o.sawServerBanner = true
		o.serverBuf = nil
		o.serverDone = true
	} else if len(o.serverBuf) >= maxBannerInspect {
		o.serverBuf = nil
		o.serverDone = true
	}
}

func (o *handshakeObserver) observeClient(p []byte) {
	if len(p) == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.clientDone {
		return
	}
	if !o.clientBannerEnd {
		remain := maxBannerInspect - len(o.clientBuf)
		if len(p) > remain {
			o.clientBuf = append(o.clientBuf, p[:remain]...)
			p = p[remain:]
		} else {
			o.clientBuf = append(o.clientBuf, p...)
			p = nil
		}
		end, ok := consumeClientSSHBanner(o.clientBuf)
		if !ok {
			if len(o.clientBuf) >= maxBannerInspect {
				o.clientBuf = nil
				o.clientDone = true
			}
			return
		}
		o.clientBannerEnd = true
		rest := append([]byte(nil), o.clientBuf[end:]...)
		o.clientBuf = nil
		if len(p) > 0 {
			rest = append(rest, p...)
		}
		p = rest
	}
	o.consumeClientPacketsLocked(p)
}

func (o *handshakeObserver) consumeClientPacketsLocked(p []byte) {
	for len(p) > 0 && !o.clientDone {
		if o.clientSkip > 0 {
			if len(p) < o.clientSkip {
				o.clientSkip -= len(p)
				return
			}
			p = p[o.clientSkip:]
			o.clientSkip = 0
			continue
		}
		need := 6 - len(o.clientBuf)
		if len(p) < need {
			o.clientBuf = append(o.clientBuf, p...)
			return
		}
		o.clientBuf = append(o.clientBuf, p[:need]...)
		p = p[need:]

		pktLen := int(binary.BigEndian.Uint32(o.clientBuf[:4]))
		msgType := o.clientBuf[5]
		o.clientBuf = o.clientBuf[:0]
		o.clientPackets++

		if pktLen < 2 || pktLen > maxKexPacketLen || o.clientPackets > maxKexPacketsInspect {
			o.clientDone = true
			return
		}
		if msgType == sshMsgNewKeys {
			o.reachedUserAuth = true
			o.clientDone = true
			return
		}
		// 6 bytes of the packet (4-byte length + pad_len + msg_type) have been consumed;
		// total unencrypted wire packet size before NEWKEYS is 4 + pktLen.
		o.clientSkip = (4 + pktLen) - 6
	}
}

func (o *handshakeObserver) summary() (sawServerBanner, reachedUserAuth bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sawServerBanner, o.reachedUserAuth
}

func hasSSHBannerLine(buf []byte) bool {
	for _, line := range bytes.Split(buf, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("SSH-2.0-")) || bytes.HasPrefix(line, []byte("SSH-1.99-")) {
			return true
		}
	}
	return false
}

func consumeClientSSHBanner(buf []byte) (int, bool) {
	offset := 0
	for offset < len(buf) {
		nl := bytes.IndexByte(buf[offset:], '\n')
		if nl < 0 {
			return 0, false
		}
		line := buf[offset : offset+nl]
		offset += nl + 1
		if bytes.HasPrefix(line, []byte("SSH-2.0-")) || bytes.HasPrefix(line, []byte("SSH-1.99-")) {
			return offset, true
		}
	}
	return 0, false
}
