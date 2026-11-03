//go:build linux

package lineage

// SocketsOf joins native TCP and descriptor tables for one current incarnation.
// Inaccessible descriptor tables are failures, never empty ownership evidence.
func SocketsOf(pid int) ([]SocketObservation, error) {
	table, err := hostProc.sockets([]Protocol{ProtocolTCP})
	if err != nil {
		return nil, err
	}
	return hostProc.socketsOf(pid, table)
}
