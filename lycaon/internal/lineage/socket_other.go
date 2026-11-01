//go:build !(darwin && cgo) && !linux

package lineage

func SocketsOf(int) ([]SocketObservation, error) { return nil, ErrUnsupported }
