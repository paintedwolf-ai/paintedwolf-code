package projectpaths

import (
	"bytes"
	"errors"
	"testing"
)

type chunkWitness struct{ largest, total int }

func (w *chunkWitness) Write(p []byte) (int, error) {
	w.largest = max(w.largest, len(p))
	w.total += len(p)
	return len(p), nil
}
func TestPathOutputBoundsWritesAndRefusesBeforeIngress(t *testing.T) {
	var chunks chunkWitness
	writer := newPathOutput(t.Context(), &chunks, 100000)
	data := bytes.Repeat([]byte("x"), 70000)
	n, err := writer.Write(data)
	if err != nil || n != len(data) || chunks.total != len(data) || chunks.largest > 32*1024 {
		t.Fatalf("unbounded path writes: n=%d largest=%d total=%d err=%v", n, chunks.largest, chunks.total, err)
	}
	before := chunks.total
	n, err = writer.Write(bytes.Repeat([]byte("x"), 30001))
	var limit *PathError
	if n != 0 || !errors.As(err, &limit) || limit.Code != "COMMAND_LIMIT_EXCEEDED" || limit.Maximum != 100000 || limit.Observed != 100001 || limit.ResourceUnit != "bytes" || chunks.total != before {
		t.Fatalf("ceiling admitted bytes or lost typed bound: n=%d limit=%+v total=%d", n, limit, chunks.total)
	}
}

func TestPathLimitCarriesExplicitUnitForUnknownIdentity(t *testing.T) {
	err := pathLimit("fixture_opaque_resource", "fixture units", 3, 4)
	var limit *PathError
	if !errors.As(err, &limit) || limit.ResourceUnit != "fixture units" || limit.LimitID != "fixture_opaque_resource" {
		t.Fatalf("unit inferred from identity: %+v", limit)
	}
}
