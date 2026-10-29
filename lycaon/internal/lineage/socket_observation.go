package lineage

import "net/netip"

// SocketObservation records a TCP kernel object and the process incarnation
// observed holding it. It grants nothing and must be refreshed at a dial/write
// boundary. A listening request alone cannot produce this fact.
type SocketObservation struct {
	Local      netip.AddrPort
	Remote     netip.AddrPort
	ResourceID string
	Listening  bool
	Holder     ProcessIdentity
}

func sameEndpoint(a, b netip.AddrPort) bool {
	return a.Port() == b.Port() && a.Addr().Unmap() == b.Addr().Unmap()
}
