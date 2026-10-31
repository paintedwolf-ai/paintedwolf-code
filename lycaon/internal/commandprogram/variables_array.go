package commandprogram

import (
	"context"
	"math"
	"sort"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// AssignScalar sets or appends a scalar variable in this frame.
func (v *Variables) AssignScalar(ctx context.Context, name, value string, appendMode bool) error {
	current := v.values[name]
	if current.array {
		return v.AssignIndex(ctx, name, 0, value, appendMode)
	}
	if !appendMode || !current.set {
		return v.Assign(ctx, name, value)
	}
	if err := v.appendable(name, current.value, value); err != nil {
		return err
	}
	size := int64(len(name) + len(current.value) + len(value) + variableBytes)
	return v.replace(ctx, name, size, func() variable {
		return variable{value: current.value + value, set: true, exported: current.exported}
	})
}

func (v *Variables) appendable(name, before, addition string) error {
	if err := assignable(v.bounds, name, addition); err != nil {
		return err
	}
	size := int64(len(before)) + int64(len(addition))
	if size > v.bounds.maximum["variable_value_bytes"] {
		return v.bounds.exceeded("variable_value_bytes", size, toolcontract.SourceSpan{})
	}
	return nil
}

// AssignIndex sets or appends element index of an indexed array variable.
func (v *Variables) AssignIndex(ctx context.Context, name string, index int64, value string, appendMode bool) error {
	if index < 0 {
		return invalidValue(toolcontract.SourceSpan{}, "a non-negative array subscript")
	}
	if err := assignable(v.bounds, name, value); err != nil {
		return err
	}
	current := v.values[name]
	before, exists := current.elements[index]
	if !current.array && current.set && index == 0 {
		before, exists = current.value, true
	}
	if appendMode {
		if err := v.appendable(name, before, value); err != nil {
			return err
		}
	}
	size := arraySize(name, current)
	if exists {
		size -= int64(len(before) + arrayElementBytes)
	}
	size += int64(len(value) + arrayElementBytes)
	if appendMode {
		size += int64(len(before))
	}
	return v.replace(ctx, name, size, func() variable {
		elements := copyElements(current)
		if appendMode {
			value = before + value
		}
		elements[index] = value
		return indexedVariable(elements, current.exported)
	})
}

// AssignArray sets or appends an indexed array variable from items.
func (v *Variables) AssignArray(ctx context.Context, name string, items []string, appendMode bool) error {
	if err := assignable(v.bounds, name, ""); err != nil {
		return err
	}
	current := v.values[name]
	start, size := int64(0), int64(len(name)+variableBytes)
	if appendMode {
		size = arraySize(name, current)
		if current.array {
			for k := range current.elements {
				if k >= start {
					if k == math.MaxInt64 {
						if len(items) > 0 {
							return v.arrayIndexOverflow(uint64(k) + uint64(len(items)))
						}
						continue
					}
					start = k + 1
				}
			}
		} else if current.set {
			start = 1
		}
	}
	if len(items) > 0 && int64(len(items)-1) > math.MaxInt64-start {
		return v.arrayIndexOverflow(uint64(start) + uint64(len(items)) - 1)
	}
	for _, item := range items {
		if err := assignable(v.bounds, name, item); err != nil {
			return err
		}
		size += int64(len(item) + arrayElementBytes)
	}
	return v.replace(ctx, name, size, func() variable {
		var elements map[int64]string
		if appendMode {
			elements = copyElements(current)
		} else {
			elements = make(map[int64]string, len(items))
		}
		for i, item := range items {
			elements[start+int64(i)] = item
		}
		return indexedVariable(elements, current.exported)
	})
}

func arraySize(name string, current variable) int64 {
	size := int64(len(name) + variableBytes)
	if current.array {
		for _, value := range current.elements {
			size += int64(len(value) + arrayElementBytes)
		}
	} else if current.set {
		size += int64(len(current.value) + arrayElementBytes)
	}
	return size
}

func copyElements(current variable) map[int64]string {
	if current.array {
		out := make(map[int64]string, len(current.elements))
		for k, value := range current.elements {
			out[k] = value
		}
		return out
	}
	if current.set {
		return map[int64]string{0: current.value}
	}
	return map[int64]string{}
}

// The entry charge covers the map, ordered projection and temporary sorting
// keys. Expansion borrows the immutable ordered projection without copying it.
const arrayElementBytes = 64

func indexedVariable(elements map[int64]string, exported bool) variable {
	keys := make([]int64, 0, len(elements))
	for key := range elements {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	ordered := make([]string, len(keys))
	for i, key := range keys {
		ordered[i] = elements[key]
	}
	return variable{elements: elements, ordered: ordered, array: true, set: true, exported: exported}
}

func (v *Variables) arrayIndexOverflow(observed uint64) *ExpansionError {
	return &ExpansionError{Code: codeOperandInvalid, Expected: v.bounds.arrayIndexRule, Limit: "array_index", Unit: "index", Maximum: math.MaxInt64, UnsignedObserved: observed}
}
