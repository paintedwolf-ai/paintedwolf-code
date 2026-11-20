// Package dns provides deterministic responses to the standard Go resolver.
package dns

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// Install replaces the test process's resolver until cleanup. answer is called
// for each IPv4 query with its normalized host name.
func Install(t *testing.T, answer func(string) []netip.Addr) {
	t.Helper()
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, server := net.Pipe()
		go serveQuery(ctx, server, answer)
		return client, nil
	}}
	t.Cleanup(func() { net.DefaultResolver = previous })
}

func serveQuery(ctx context.Context, conn net.Conn, answer func(string) []netip.Addr) {
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	var prefix [2]byte
	if _, err := io.ReadFull(conn, prefix[:]); err != nil {
		return
	}
	packet := make([]byte, binary.BigEndian.Uint16(prefix[:]))
	if _, err := io.ReadFull(conn, packet); err != nil {
		return
	}
	var request dnsmessage.Message
	if err := request.Unpack(packet); err != nil || len(request.Questions) != 1 {
		return
	}
	question := request.Questions[0]
	response := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID: request.ID, Response: true, Authoritative: true,
			RecursionDesired: request.RecursionDesired, RecursionAvailable: true,
		},
		Questions: request.Questions,
	}
	if question.Type == dnsmessage.TypeA {
		addresses := answer(strings.TrimSuffix(strings.ToLower(question.Name.String()), "."))
		if len(addresses) == 0 {
			response.RCode = dnsmessage.RCodeNameError
		}
		for _, addr := range addresses {
			response.Answers = append(response.Answers, dnsmessage.Resource{
				Header: dnsmessage.ResourceHeader{Name: question.Name, Class: dnsmessage.ClassINET},
				Body:   &dnsmessage.AResource{A: addr.As4()},
			})
		}
	}
	wire, err := response.Pack()
	if err != nil {
		return
	}
	binary.BigEndian.PutUint16(prefix[:], uint16(len(wire)))
	_, _ = conn.Write(append(prefix[:], wire...))
}
