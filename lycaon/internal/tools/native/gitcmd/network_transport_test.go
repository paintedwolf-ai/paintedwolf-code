package gitcmd

import (
	"testing"

	"github.com/lycaon/lycaon/internal/egressproxy"
)

// Remote admission decides the connection Git will open through the proxy,
// so the confined launch's own dial meets that decision.
func TestRemoteAdmissionNamesTheConnectionGitOpens(t *testing.T) {
	for _, tc := range []struct {
		url       string
		transport egressproxy.Transport
		host      string
		port      uint16
	}{
		{"https://Git.Example.test/org/repo.git", egressproxy.TransportHTTPConnect, "git.example.test", 443},
		{"https://git.example.test:8443/repo.git", egressproxy.TransportHTTPConnect, "git.example.test", 8443},
		{"http://git.example.test/repo.git", egressproxy.TransportHTTPRequest, "git.example.test", 80},
		{"ssh://git@git.example.test:2222/repo.git", egressproxy.TransportSocksTCP, "git.example.test", 2222},
		{"git@git.example.test:org/repo.git", egressproxy.TransportSocksTCP, "git.example.test", 22},
		{"git://git.example.test/repo.git", egressproxy.TransportSocksTCP, "git.example.test", 9418},
		{"https://[2001:db8::1]/repo.git", egressproxy.TransportHTTPConnect, "2001:db8::1", 443},
	} {
		ep := remoteTransport(tc.url, remoteNetworkHost(tc.url))
		if ep.Transport != tc.transport || ep.Host != tc.host || ep.Port != tc.port {
			t.Errorf("%s: got %s, want %s://%s:%d", tc.url, ep, tc.transport, tc.host, tc.port)
		}
	}
}
