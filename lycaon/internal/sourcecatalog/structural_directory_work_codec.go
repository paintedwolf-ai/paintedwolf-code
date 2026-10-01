package sourcecatalog

import (
	"encoding/binary"
	"errors"
	"math"
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/repochange"
)

// The fixed prefix holds ten 64-bit scalars followed by the completion byte.
const directoryWorkScalars = 10

// Scratch records omit the path already stored in SQLite's primary key.
func encodeDirectoryWorkRecord(record structuralDirectory) ([]byte, error) {
	observation := record.observation
	body := make([]byte, 0, directoryWorkScalars*8+16+len(observation.Failure)+len(observation.Epoch.BootID))
	for _, value := range []uint64{record.page, directoryWorkIntBits(observation.Sequence), directoryWorkIntBits(observation.FirstListed), directoryWorkIntBits(int64(observation.Entries)), observation.Invalidation, observation.Epoch.Value, directoryWorkIntBits(observation.Observed.Unix()), directoryWorkIntBits(int64(observation.Observed.Nanosecond())), directoryWorkIntBits(observation.Stamp.Modified), directoryWorkIntBits(observation.Stamp.Changed)} {
		body = binary.LittleEndian.AppendUint64(body, value)
	}
	complete := byte(0)
	if observation.Complete {
		complete = 1
	}
	body = append(body, complete)
	body = binary.AppendUvarint(body, uint64(len(observation.Failure)))
	body = append(body, observation.Failure...)
	body = append(body, observation.Epoch.BootID...)
	if len(body) > structuralDirectoryRecordLimit {
		return nil, pagedview.ErrFrameSize
	}
	return body, nil
}
func decodeDirectoryWorkRecord(key string, body []byte) (structuralDirectory, error) {
	const completeAt = directoryWorkScalars * 8
	if len(body) < completeAt+2 || len(body) > structuralDirectoryRecordLimit {
		return structuralDirectory{}, pagedview.ErrFrameSize
	}
	number := func(at int) uint64 { return binary.LittleEndian.Uint64(body[at*8 : at*8+8]) }
	failureLength, n := binary.Uvarint(body[completeAt+1:])
	if n <= 0 || body[completeAt] > 1 {
		return structuralDirectory{}, errors.New("invalid scratch directory record")
	}
	remaining := len(body) - completeAt - 1 - n
	if remaining < 0 {
		return structuralDirectory{}, errors.New("invalid scratch directory length")
	}
	if failureLength > uint64(remaining) {
		return structuralDirectory{}, errors.New("invalid scratch directory length")
	}
	entries := directoryWorkSignedBits(number(3))
	nanoseconds := directoryWorkSignedBits(number(7))
	if entries < 0 || entries > math.MaxInt || nanoseconds < 0 || nanoseconds >= 1e9 || failureLength > math.MaxInt {
		return structuralDirectory{}, errors.New("invalid scratch directory scalar")
	}
	start := completeAt + 1 + n
	return structuralDirectory{page: number(0), observation: DirectoryObservation{
		Path: key, Sequence: directoryWorkSignedBits(number(1)), FirstListed: directoryWorkSignedBits(number(2)), Entries: int(entries), Invalidation: number(4),
		Complete: body[completeAt] != 0, Observed: time.Unix(directoryWorkSignedBits(number(6)), nanoseconds).UTC(),
		Stamp:   DirectoryStamp{Modified: directoryWorkSignedBits(number(8)), Changed: directoryWorkSignedBits(number(9))},
		Failure: string(body[start : start+int(failureLength)]),
		Epoch:   repochange.Epoch{Value: number(5), BootID: string(body[start+int(failureLength):])},
	}}, nil
}

func directoryWorkIntBits(value int64) uint64 {
	return uint64(value) //nolint:gosec // G115: Scratch serialization preserves the signed two's-complement bits, including pre-epoch dates.
}
func directoryWorkSignedBits(value uint64) int64 {
	return int64(value) //nolint:gosec // G115: This reverses directoryWorkIntBits without changing its signed representation.
}
