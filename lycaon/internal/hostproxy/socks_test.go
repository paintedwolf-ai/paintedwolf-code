package hostproxy

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

type shortWriter struct{ net.Conn }

func (connection shortWriter) Write(value []byte) (int, error) {
	if len(value) > 2 {
		value = value[:2]
	}
	return connection.Conn.Write(value)
}

func TestSOCKSAuthenticatesAndCarriesOnlyPinnedNumericAddressThroughShortWrites(t *testing.T) {
	for _, literal := range []string{"192.0.2.7:8443", "[2001:db8::7]:443"} {
		t.Run(literal, func(t *testing.T) {
			address := netip.MustParseAddrPort(literal)
			client, server := net.Pipe()
			defer func() { _ = client.Close() }()
			testutil.FailErr(t, "bound SOCKS client", client.SetDeadline(time.Now().Add(time.Second)))
			done := make(chan error, 1)
			go func() {
				defer func() { _ = server.Close() }()
				done <- func() error {
					var greeting [3]byte
					if _, err := io.ReadFull(server, greeting[:]); err != nil {
						return err
					}
					if greeting != [3]byte{5, 1, 2} {
						return fmt.Errorf("SOCKS auth was not required")
					}
					if err := writePacket(shortWriter{server}, []byte{5, 2}); err != nil {
						return err
					}
					var auth [2]byte
					if _, err := io.ReadFull(server, auth[:]); err != nil {
						return err
					}
					user := make([]byte, int(auth[1]))
					if _, err := io.ReadFull(server, user); err != nil {
						return err
					}
					var length [1]byte
					if _, err := io.ReadFull(server, length[:]); err != nil {
						return err
					}
					password := make([]byte, int(length[0]))
					if _, err := io.ReadFull(server, password); err != nil {
						return err
					}
					if auth[0] != 1 || string(user) != "alice" || string(password) != "private-secret" {
						return fmt.Errorf("SOCKS auth payload mismatch")
					}
					if err := writePacket(shortWriter{server}, []byte{1, 0}); err != nil {
						return err
					}
					var header [4]byte
					if _, err := io.ReadFull(server, header[:]); err != nil {
						return err
					}
					expectedKind, size := byte(1), 4
					if address.Addr().Is6() {
						expectedKind, size = 4, 16
					}
					if header != [4]byte{5, 1, 0, expectedKind} {
						return fmt.Errorf("SOCKS asked for a domain or changed its address family")
					}
					target := make([]byte, size+2)
					if _, err := io.ReadFull(server, target); err != nil {
						return err
					}
					ip, ok := netip.AddrFromSlice(target[:size])
					if !ok || ip != address.Addr() || binary.BigEndian.Uint16(target[size:]) != address.Port() {
						return fmt.Errorf("SOCKS changed the pinned destination")
					}
					return writePacket(shortWriter{server}, []byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				}()
			}()
			testutil.FailErr(t, "negotiate pinned SOCKS", socks(shortWriter{client}, url.UserPassword("alice", "private-secret"), address))
			testutil.FailErr(t, "inspect SOCKS server contract", <-done)
		})
	}
}

func TestSOCKSRefusesAuthDowngradeMalformedAndDeniedReplies(t *testing.T) {
	for _, response := range [][]byte{{5, 0}, {4, 2}, {5, 255}} {
		client, server := net.Pipe()
		testutil.FailErr(t, "bound malformed SOCKS", client.SetDeadline(time.Now().Add(time.Second)))
		go func() {
			defer func() { _ = server.Close() }()
			var greeting [3]byte
			_, _ = io.ReadFull(server, greeting[:])
			_, _ = server.Write(response)
		}()
		err := socks(client, url.UserPassword("alice", "private-secret"), netip.MustParseAddrPort("192.0.2.7:443"))
		_ = client.Close()
		if err != ProxyRejected {
			t.Fatalf("auth downgrade = %v", err)
		}
	}
	for _, reply := range [][]byte{{5, 2, 0, 1}, {4, 0, 0, 1}, {5, 0, 1, 1}, {5, 0, 0, 9}} {
		client, server := net.Pipe()
		testutil.FailErr(t, "bound denied SOCKS", client.SetDeadline(time.Now().Add(time.Second)))
		go func() {
			defer func() { _ = server.Close() }()
			var greeting [3]byte
			_, _ = io.ReadFull(server, greeting[:])
			_, _ = server.Write([]byte{5, 0})
			var request [10]byte
			_, _ = io.ReadFull(server, request[:])
			_, _ = server.Write(reply)
		}()
		err := socks(client, nil, netip.MustParseAddrPort("192.0.2.7:443"))
		_ = client.Close()
		if err == nil {
			t.Fatal("invalid SOCKS reply was accepted")
		}
	}
}
