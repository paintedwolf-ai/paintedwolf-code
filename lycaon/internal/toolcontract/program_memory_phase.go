package toolcontract

import "context"

type MemoryPhase string

const (
	ReviewEncoding   MemoryPhase = "review_encoding"
	ReviewDecoding   MemoryPhase = "review_decoding"
	InputScreening   MemoryPhase = "input_screening"
	ArgumentSnapshot MemoryPhase = "argument_snapshot"
	InputProvenance  MemoryPhase = "input_provenance"
	QueryDecoding    MemoryPhase = "query_decoding"
)

type memoryPhaseKey struct{}

// WithMemoryPhase identifies the host stage responsible for a reservation.
func WithMemoryPhase(ctx context.Context, phase MemoryPhase) context.Context {
	return context.WithValue(ctx, memoryPhaseKey{}, phase)
}

func memoryPhase(ctx context.Context) MemoryPhase {
	phase, _ := ctx.Value(memoryPhaseKey{}).(MemoryPhase)
	return phase
}
