package api

import (
	"net"
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/version"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Server) handleGetHost(w http.ResponseWriter, r *http.Request) {
	httpio.WriteJSON(w, http.StatusOK, wire.HostInfo{
		HostID:          s.hostIdentity.HostID,
		HostPublicKey:   s.hostIdentity.EncodedPublicKey(),
		ProductVersion:  version.Version,
		ContractVersion: contractVersion,
		Caller:          requestscope.Caller(r).Wire(),
		Capabilities:    connectionCapabilities(r),
	})
}

// Capabilities use the peer address reported by the kernel.
func connectionCapabilities(r *http.Request) []wire.HostCapability {
	capabilities := []wire.HostCapability{}
	if peerIsLoopback(r.RemoteAddr) {
		capabilities = append(capabilities, wire.HostCapabilitySharedDevice)
	}
	return capabilities
}

func peerIsLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	return egress.LoopbackLiteral(host)
}
