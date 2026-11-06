//go:build !(darwin && cgo) && !linux

package lineage

import (
	"net/netip"
	"os"
)

func pipePeerHandle(*os.File) (uint64, error) { return 0, ErrUnsupported }

func pipeHasWriters(*os.File) bool { return false }

func pipeHolders([]uint64) ([][]int, error) { return nil, ErrUnsupported }

func peerPID(netip.AddrPort, netip.AddrPort) (int, bool) { return 0, false }

func listeners(ListenerQuery) (ListenerTable, error) { return ListenerTable{}, ErrUnsupported }

func livePIDs() ([]int, error) { return nil, ErrUnsupported }

func socketWalker() func(int) ([]SocketObservation, error) { return SocketsOf }

func unreadableByThisUser(int, error) bool { return false }

func unreadHoldersExcluded(netip.AddrPort, endpointHolders) bool { return false }

const supported = false
