//go:build darwin

package confine_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
)

func TestDarwinSessionScratchAllowsSocketBindWithIndependentConnectAuthority(t *testing.T) {
	self := requireLiveBoundary(t)
	own, other := shortTempDir(t), shortTempDir(t)
	c := confine.Confinement{SessionScratchRoot: own, GrantedWriteRoots: []string{other}, Network: confine.NetworkDeny}
	const bind = `IO::Socket::UNIX->new(Type => SOCK_STREAM, Local => $ARGV[0], Listen => 1) or die "bind: $!";`
	const rejectedBind = `my $sock = IO::Socket::UNIX->new(Type => SOCK_STREAM, Local => $ARGV[0], Listen => 1); exit(!$sock && ($!{EPERM} || $!{EACCES}) ? 0 : 1);`
	const rejectedConnect = `my $sock = IO::Socket::UNIX->new(Type => SOCK_STREAM, Peer => $ARGV[0]); exit(!$sock && ($!{EPERM} || $!{EACCES}) ? 0 : 1);`
	for _, test := range []struct{ name, program, path string }{
		{"private bind", bind, filepath.Join(own, "published")},
		{"shared write grant", rejectedBind, filepath.Join(other, "shared")},
		{"scratch service connection", rejectedConnect, filepath.Join(own, "host")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.program == rejectedConnect {
				listenUnix(t, test.path)
			}
			if code, out := confinedRun(t, self, c, "/usr/bin/perl", "-MIO::Socket::UNIX", "-MSocket", "-e", test.program, test.path); code != 0 {
				t.Fatalf("socket operation exit %d: %s", code, out)
			}
		})
	}
}
