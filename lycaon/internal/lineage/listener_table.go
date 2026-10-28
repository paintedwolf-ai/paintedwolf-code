package lineage

import "net/netip"

// kernelListener is one listening socket from a host-wide kernel table, keyed
// by the opaque handle the platform's per-process descriptor walk also reports.
type kernelListener struct {
	Protocol Protocol
	Local    netip.AddrPort
	UID      uint32
	Handle   uint64
}

// resolveListeners attaches to each row every process whose descriptors hold
// its handle. A process whose descriptors cannot be read contributes nothing,
// so its sockets keep an empty holder list rather than disappearing.
func resolveListeners(rows []kernelListener, pids []int, handlesOf func(pid int) []uint64) []Listener {
	wanted := make(map[uint64]bool, len(rows))
	for _, row := range rows {
		wanted[row.Handle] = true
	}
	holders := socketHolders(wanted, pids, handlesOf)
	out := make([]Listener, 0, len(rows))
	for _, row := range rows {
		out = append(out, Listener{Protocol: row.Protocol, Addr: row.Local, UID: row.UID, PIDs: holders[row.Handle]})
	}
	return out
}

// socketHolders maps each wanted handle to the processes holding it, once each.
func socketHolders(wanted map[uint64]bool, pids []int, handlesOf func(pid int) []uint64) map[uint64][]int {
	out := map[uint64][]int{}
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		held := map[uint64]bool{}
		for _, handle := range handlesOf(pid) {
			if wanted[handle] && !held[handle] {
				held[handle] = true
				out[handle] = append(out[handle], pid)
			}
		}
	}
	return out
}
