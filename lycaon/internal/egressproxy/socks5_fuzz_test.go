package egressproxy

import (
	"bytes"
	"testing"
)

func FuzzParseSocksAddress(f *testing.F) {
	f.Add(byte(0x03), []byte("example.com"))
	f.Add(byte(0x01), []byte{1, 2, 3, 4})
	f.Add(byte(0x04), bytes.Repeat([]byte{0}, 16))
	f.Fuzz(func(t *testing.T, atyp byte, addr []byte) {
		_, _ = parseSocksAddress(atyp, addr)
	})
}

func FuzzParseHTTPEndpoint(f *testing.F) {
	f.Add("example.com:443", string(TransportHTTPConnect))
	f.Add("[::1]", string(TransportHTTPRequest))
	f.Fuzz(func(t *testing.T, hostport, transport string) {
		_, _ = ParseHTTPEndpoint(hostport, Transport(transport))
	})
}
