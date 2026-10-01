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

func listeners() ([]Listener, error) { return nil, ErrUnsupported }

const supported = false
