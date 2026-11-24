package sshproxy

import (
	"encoding/binary"
	"testing"
)

func buildUnencryptedPacket(msgType byte, payloadLen int) []byte {
	padLen := byte(4)
	pktLen := 1 + 1 + payloadLen + int(padLen)
	buf := make([]byte, 4+pktLen)
	binary.BigEndian.PutUint32(buf[:4], uint32(pktLen))
	buf[4] = padLen
	buf[5] = msgType
	return buf
}

func TestHandshakeObserverTracksBannerAndNewKeysAcrossFragments(t *testing.T) {
	obs := &handshakeObserver{}
	serverStream := []byte("Welcome banner\r\nSSH-2.0-OpenSSH_9.9\r\npayload")
	for _, b := range serverStream {
		obs.observeServer([]byte{b})
	}

	var clientStream []byte
	clientStream = append(clientStream, []byte("SSH-2.0-OpenSSH_9.9\r\n")...)
	clientStream = append(clientStream, buildUnencryptedPacket(20, 64)...) // SSH_MSG_KEXINIT
	clientStream = append(clientStream, buildUnencryptedPacket(30, 36)...) // SSH_MSG_KEX_ECDH_INIT

	for _, b := range clientStream {
		obs.observeClient([]byte{b})
	}
	sawBanner, reachedAuth := obs.summary()
	if !sawBanner || reachedAuth {
		t.Fatalf("before NEWKEYS: sawBanner=%v reachedAuth=%v, want true/false", sawBanner, reachedAuth)
	}

	newKeys := buildUnencryptedPacket(sshMsgNewKeys, 0)
	for _, b := range newKeys {
		obs.observeClient([]byte{b})
	}
	sawBanner, reachedAuth = obs.summary()
	if !sawBanner || !reachedAuth {
		t.Fatalf("after NEWKEYS: sawBanner=%v reachedAuth=%v, want true/true", sawBanner, reachedAuth)
	}
}

func TestHandshakeObserverRejectsNonSSHTraffic(t *testing.T) {
	obs := &handshakeObserver{}
	obs.observeServer([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
	obs.observeClient([]byte("GET / HTTP/1.1\r\n\r\n"))
	sawBanner, reachedAuth := obs.summary()
	if sawBanner || reachedAuth {
		t.Fatalf("non-SSH traffic produced sawBanner=%v reachedAuth=%v", sawBanner, reachedAuth)
	}
}
