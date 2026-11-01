package query

import (
	"encoding/json"
	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
	"math"

	"github.com/itchyny/gojq"
	"github.com/lycaon/lycaon/internal/tools"
)

func (in *inputs) retainCurrent() func() {
	if in.stream != nil {
		return in.stream.Retain()
	}
	return nil
}

// slurpInputs holds input leases until the aggregate evaluation completes.
func (r *jqRun) slurpInputs() ([]any, func(), error) {
	var values []any
	var leases []func()
	work := &queryWorking{inv: r.inv}
	close := func() {
		for _, release := range leases {
			release()
		}
		work.Close()
	}
	maximum := r.inv.owners.limit("query_value_bytes")
	size, borrowed := int64(2), int64(0)
	overhead := 512*r.inv.owners.limit("query_depth") + 256
	if err := work.resize(overhead); err != nil {
		return nil, nil, err
	}
	if size > maximum {
		close()
		return nil, nil, tools.CommandLimitExceeded("query_value_bytes", maximum, size)
	}
	for value, ok := r.stream.Next(); ok; value, ok = r.stream.Next() {
		comma := int64(0)
		if len(values) > 0 {
			comma = 1
		}
		next, err := nativejq.JSONSize(r.inv.ctx, value, max(0, maximum-size-comma))
		if err != nil {
			close()
			return nil, nil, err
		}
		if next > maximum-size-comma {
			close()
			return nil, nil, tools.CommandLimitExceeded("query_value_bytes", maximum, size+comma+next)
		}
		size += comma + next
		if retained := r.stream.retainCurrent(); retained != nil {
			leases = append(leases, retained)
		} else {
			borrowed += valueStorageBytes(value, math.MaxInt64)
		}
		if len(values) == cap(values) {
			capacity := max(4, 2*cap(values))
			if err := work.resize(overhead + borrowed + int64(cap(values)+capacity)*16); err != nil {
				close()
				return nil, nil, err
			}
			grown := make([]any, len(values), capacity)
			copy(grown, values)
			values = grown
		}
		if err := work.resize(overhead + borrowed + int64(cap(values))*16); err != nil {
			close()
			return nil, nil, err
		}
		values = append(values, value)
	}
	if r.stream.stop != nil {
		close()
		return nil, nil, r.stream.stop
	}
	if values == nil {
		values = []any{}
	}
	return values, close, nil
}

func valueStorageBytes(value any, remaining int64) int64 {
	total := int64(64)
	add := func(value any) {
		if total <= remaining {
			total += valueStorageBytes(value, remaining-total)
		}
	}
	switch value := value.(type) {
	case string:
		total += int64(len(value))
	case json.Number:
		total += int64(len(value))
	case []any:
		total += int64(len(value)) * 16
		for _, child := range value {
			add(child)
		}
	case *gojq.OrderedObject:
		value.Range(func(key string, child any) bool {
			total += 128 + int64(len(key))
			add(child)
			return total <= remaining
		})
	}
	return total
}
