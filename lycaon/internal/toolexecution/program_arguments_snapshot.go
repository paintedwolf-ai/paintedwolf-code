package toolexecution

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// nativeArguments holds detached review inputs until admission, held review and
// the complete owner call have released them. Strings are immutable; maps and
// slices are cloned by the existing JSON value implementation.
type nativeArguments struct {
	values map[string]any
	leases []func()
}

func prepareNativeArguments(ctx context.Context, admission *toolcontract.ProgramMemory, values map[string]any) (*nativeArguments, error) {
	if ctx == nil || admission == nil {
		return nil, fmt.Errorf("native review inputs require retained state admission")
	}
	snapshot := &nativeArguments{}
	if err := snapshot.reserve(ctx, admission, values); err != nil {
		snapshot.close()
		return nil, err
	}
	snapshot.values = jsonvalue.CloneMap(values)
	return snapshot, nil
}

// Reserve each traversal frame before descending. This also bounds cyclic host
// values through the same aggregate ceiling instead of an unmetered recursive
// walk. The conservative payload bound covers JSON escaping, review projections
// and the detached map while overlapping review/dispatch facts remain live.
func (s *nativeArguments) reserve(ctx context.Context, admission *toolcontract.ProgramMemory, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	const copies = int64(8)
	bound := int64(2048)
	add := func(size int64) bool {
		if size < 0 || size > (math.MaxInt64-bound)/copies {
			return false
		}
		bound += size * copies
		return true
	}
	switch v := value.(type) {
	case string:
		if !add(int64(len(v))) {
			return fmt.Errorf("native review string bound overflow")
		}
	case map[string]any:
		if !add(int64(len(v)) * 256) {
			return fmt.Errorf("native review object bound overflow")
		}
	case []any:
		if !add(int64(len(v)) * 32) {
			return fmt.Errorf("native review list bound overflow")
		}
	case []string:
		if !add(int64(len(v)) * 32) {
			return fmt.Errorf("native review list bound overflow")
		}
	case json.Number:
		if !add(int64(len(v))) {
			return fmt.Errorf("native review number bound overflow")
		}
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("non-JSON native review number")
		}
	case float32:
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return fmt.Errorf("non-JSON native review number")
		}
	default:
		return fmt.Errorf("native review inputs must be JSON values")
	}
	release, err := admission.ReserveProgramState(ctx, bound)
	if err != nil {
		return err
	}
	s.leases = append(s.leases, release)
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if err := s.reserve(ctx, admission, key); err != nil {
				return err
			}
			if err := s.reserve(ctx, admission, item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range v {
			if err := s.reserve(ctx, admission, item); err != nil {
				return err
			}
		}
	case []string:
		for _, item := range v {
			if err := s.reserve(ctx, admission, item); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func (s *nativeArguments) close() {
	s.values = nil
	for i := len(s.leases) - 1; i >= 0; i-- {
		s.leases[i]()
	}
	s.leases = nil
}
