package commandprogram

import (
	"math"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// BatchBudget bounds one child while leaving capacity for its producer and siblings.
type BatchBudget struct {
	ArgumentBytes, Fields                    int64
	state, maximum, held, perField, parallel int64
	catalogBytes, catalogFields              int64
}

func NewBatchBudget(memory *toolcontract.ProgramMemory, directory string, argumentBytes, fields, parallel int64) BatchBudget {
	result := BatchBudget{ArgumentBytes: argumentBytes, Fields: fields, state: math.MaxInt64, parallel: max(1, parallel), catalogBytes: argumentBytes, catalogFields: fields}
	if memory == nil {
		return result
	}
	capacity := memory.BatchStateCapacity()
	result.maximum, result.held = capacity.Maximum, capacity.Held
	result.state = capacity.Available / (result.parallel + 1)
	// The review keeps operand and access-target collections through capture and admission.
	result.perField = 4096 + 8*int64(len(directory))
	remaining := max(0, result.state-(256<<10))
	result.ArgumentBytes = min(argumentBytes, remaining/16)
	result.Fields = min(fields, remaining/result.perField)
	return result
}
func (b BatchBudget) Fits(bytes, fields int64) bool {
	return bytes <= b.ArgumentBytes && fields <= b.Fields && b.cost(bytes, fields) <= b.state
}
func (b BatchBudget) cost(bytes, fields int64) int64 {
	if b.perField == 0 {
		return 0
	}
	if bytes > (math.MaxInt64-(256<<10))/16 {
		return math.MaxInt64
	}
	total := int64(256<<10) + 16*bytes
	if fields > (math.MaxInt64-total)/b.perField {
		return math.MaxInt64
	}
	return total + fields*b.perField
}
func (b BatchBudget) Refusal(bytes, fields int64) error {
	if bytes > b.catalogBytes {
		return &ProgramLimitError{Limit: "argv_bytes", Maximum: b.catalogBytes, Observed: bytes}
	}
	if fields > b.catalogFields {
		return &ProgramLimitError{Limit: "expanded_fields", Maximum: b.catalogFields, Observed: fields}
	}
	cost := b.cost(bytes, fields)
	observed := int64(math.MaxInt64)
	if cost <= (math.MaxInt64-b.held)/(b.parallel+1) {
		observed = b.held + cost*(b.parallel+1)
	}
	return &ProgramLimitError{Limit: "program_state_bytes", Maximum: b.maximum, Observed: observed}
}
