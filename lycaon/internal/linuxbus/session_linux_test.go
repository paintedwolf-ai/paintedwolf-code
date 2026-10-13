//go:build linux

package linuxbus

import (
	"reflect"
	"strings"
	"testing"
)

func TestSessionAddressesPreserveNativeSessionSelection(t *testing.T) {
	for _, tc := range []struct {
		name, address, runtime string
		want                   []string
	}{
		{"default runtime", "", "", []string{"/run/user/1234/bus"}},
		{"custom runtime", "", "/custom/session", []string{"/custom/session/bus"}},
		{"explicit path", "unix:path=/session/one", "/other", []string{"/session/one"}},
		{"escaped path and guid", "unix:path=/session/a%20b,guid=0123456789abcdef0123456789abcdef", "", []string{"/session/a b"}},
		{"abstract socket", "unix:abstract=session%2Cone", "", []string{"\x00session,one"}},
		{"address alternatives", "unix:path=/one;unix:abstract=/two", "", []string{"/one", "\x00/two"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sessionSocketPaths(tc.address, tc.runtime, 1234)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("session paths = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestSessionAddressesRefuseInvalidAndNonlocalTransports(t *testing.T) {
	for _, address := range []string{
		"autolaunch:", "tcp:host=localhost,port=1234", "unix:runtime=yes", "unix:path=relative",
		"unix:path=/one,path=/two", "unix:path=/one,abstract=two", "unix:path=", "unix:guid=0123",
		"unix:path=/one,guid=bad", "unix:path=/a%00b", "unix:path=/a%0db", "unix:path=/a%0ab",
		"unix:path=/a%", "unix:path=/a%zz", "unix:path=/a b", "unix:path=/a,b", "unix:path=/one;",
		strings.Repeat("x", (64<<10)+1), strings.Repeat("unix:path=/one;", 16) + "unix:path=/last",
	} {
		if paths, err := sessionSocketPaths(address, "", 1234); err == nil || paths != nil {
			t.Fatalf("invalid address %q supplied %q, %v", address, paths, err)
		}
	}
	for _, runtime := range []string{"relative", "/a\x00b", "/a\nb"} {
		if paths, err := sessionSocketPaths("", runtime, 1234); err == nil || paths != nil {
			t.Fatalf("invalid runtime %q supplied %q, %v", runtime, paths, err)
		}
	}
}
