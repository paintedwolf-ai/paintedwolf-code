package documentcore

import (
	varint "encoding/binary"
	"errors"
	"math"
)

// VectorClocks decodes client clocks from a v1 CRDT state vector.
func VectorClocks(encoded []byte) (map[uint32]uint32, error) {
	read := func() (uint64, bool) {
		value, size := varint.Uvarint(encoded)
		if size <= 0 {
			return 0, false
		}
		encoded = encoded[size:]
		return value, true
	}
	count, ok := read()
	if !ok || count > uint64(len(encoded)/2) {
		return nil, errors.New("invalid document state vector")
	}
	clocks := make(map[uint32]uint32, count)
	for range count {
		client, clientOK := read()
		clock, clockOK := read()
		if !clientOK || !clockOK || client > math.MaxUint32 || clock > math.MaxUint32 {
			return nil, errors.New("invalid document state vector")
		}
		clocks[uint32(client)] = uint32(clock)
	}
	if len(encoded) != 0 {
		return nil, errors.New("invalid document state vector")
	}
	return clocks, nil
}
