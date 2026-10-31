package lineage

import "net/netip"

// SocketObservation records a TCP or UDP kernel object and the process
// incarnation observed holding it. It grants nothing and must be refreshed at a
// dial/write boundary. A listening request alone cannot produce this fact.
type SocketObservation struct {
	Protocol   Protocol
	Local      netip.AddrPort
	Remote     netip.AddrPort
	ResourceID string
	Listening  bool
	Holder     ProcessIdentity
	// handle is the raw kernel handle the host-wide socket table also reports.
	// It stays unexported so kernel addresses never reach logs or receipts.
	handle uint64
}

func sameEndpoint(a, b netip.AddrPort) bool {
	return a.Port() == b.Port() && a.Addr().Unmap() == b.Addr().Unmap()
}
