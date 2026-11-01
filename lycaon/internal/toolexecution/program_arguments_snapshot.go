package toolexecution

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// nativeArguments owns the containers and scalar backing retained through review.
type nativeArguments struct {
	values map[string]any
	leases []func()
}

func prepareNativeArguments(ctx context.Context, admission *toolcontract.ProgramMemory, values map[string]any) (*nativeArguments, error) {
	if ctx == nil || admission == nil {
		return nil, fmt.Errorf("native review inputs require retained state admission")
	}
	snapshot := &nativeArguments{}
	value, err := snapshot.clone(ctx, admission, values, 0)
	if err != nil {
		snapshot.close()
		return nil, err
	}
	snapshot.values = value.(map[string]any)
	return snapshot, nil
}
func (s *nativeArguments) clone(ctx context.Context, memory *toolcontract.ProgramMemory, value any, depth int) (any, error) {
	if depth > 1024 {
		return nil, fmt.Errorf("native review nesting exceeds 1024")
	}
	frame, err := memory.ReserveProgramState(ctx, 512)
	if err != nil {
		return nil, err
	}
	defer frame()
	bound := int64(128)
	switch value := value.(type) {
	case string:
		bound += int64(len(value))
	case json.Number:
		bound += int64(len(value))
	case map[string]any:
		bound += int64(len(value)) * 256
	case []any:
		bound += int64(len(value)) * 32
	case []string:
		bound += int64(len(value)) * 32
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("non-JSON native review number")
		}
	case float32:
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, fmt.Errorf("non-JSON native review number")
		}
	default:
		return nil, fmt.Errorf("native review inputs must be JSON values")
	}
	release, err := memory.ReserveProgramState(ctx, bound)
	if err != nil {
		return nil, err
	}
	s.leases = append(s.leases, release)
	switch value := value.(type) {
	case string:
		return strings.Clone(value), nil
	case json.Number:
		return json.Number(strings.Clone(string(value))), nil
	case map[string]any:
		if value == nil {
			return value, nil
		}
		result := make(map[string]any, len(value))
		for key, child := range value {
			name, err := s.clone(ctx, memory, key, depth+1)
			if err != nil {
				return nil, err
			}
			cloned, err := s.clone(ctx, memory, child, depth+1)
			if err != nil {
				return nil, err
			}
			result[name.(string)] = cloned
		}
		return result, nil
	case []any:
		if value == nil {
			return value, nil
		}
		result := make([]any, len(value))
		for i, child := range value {
			cloned, err := s.clone(ctx, memory, child, depth+1)
			if err != nil {
				return nil, err
			}
			result[i] = cloned
		}
		return result, nil
	case []string:
		var result []string
		if value != nil {
			result = make([]string, len(value))
		}
		for i, child := range value {
			cloned, err := s.clone(ctx, memory, child, depth+1)
			if err != nil {
				return nil, err
			}
			result[i] = cloned.(string)
		}
		return result, nil
	default:
		return value, nil
	}
}
func (s *nativeArguments) close() {
	s.values = nil
	for i := len(s.leases) - 1; i >= 0; i-- {
		s.leases[i]()
	}
	s.leases = nil
}
