package gojq

import "sort"

// OrderedObject is a JSON object that keeps its keys in insertion order, as jq does:
// construction, parsing and assignment append new keys, and encoding,
// to_entries and keys_unsorted follow that order. keys, comparison and
// sorted output still order keys by codepoint.
//
// An OrderedObject is a value: operations that change one return a new OrderedObject, and
// an OrderedObject reachable from a value is never mutated after construction.
type OrderedObject struct {
	keys   []string
	values map[string]any
}

// NewObject returns an empty object with room for n keys.
func NewObject(n int) *OrderedObject {
	return &OrderedObject{keys: make([]string, 0, n), values: make(map[string]any, n)}
}

// ObjectOf builds an object from alternating key and value arguments, in order.
func ObjectOf(pairs ...any) *OrderedObject {
	o := NewObject(len(pairs) / 2)
	for i := 0; i+1 < len(pairs); i += 2 {
		o.Set(pairs[i].(string), pairs[i+1])
	}
	return o
}

// Len is the number of keys.
func (o *OrderedObject) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Get returns the value at k.
func (o *OrderedObject) Get(k string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.values[k]
	return v, ok
}

// Keys returns the keys in insertion order. The slice must not be modified.
func (o *OrderedObject) Keys() []string {
	if o == nil {
		return nil
	}
	return o.keys
}

// SortedKeys returns the keys in codepoint order.
func (o *OrderedObject) SortedKeys() []string {
	w := make([]string, len(o.keys))
	copy(w, o.keys)
	sort.Strings(w)
	return w
}

// Set assigns k, appending it when new. Only an object still under
// construction may be set.
func (o *OrderedObject) Set(k string, v any) {
	if _, ok := o.values[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.values[k] = v
}

// Clone returns an independent copy with room for extra more keys.
func (o *OrderedObject) Clone(extra int) *OrderedObject {
	c := NewObject(o.Len() + extra)
	for _, k := range o.Keys() {
		c.Set(k, o.values[k])
	}
	return c
}

// Without returns a copy that omits k, keeping the remaining order.
func (o *OrderedObject) Without(k string) *OrderedObject {
	c := NewObject(o.Len())
	for _, key := range o.Keys() {
		if key != k {
			c.Set(key, o.values[key])
		}
	}
	return c
}

// Range calls f for each key in insertion order until it returns false.
func (o *OrderedObject) Range(f func(k string, v any) bool) {
	for _, k := range o.Keys() {
		if !f(k, o.values[k]) {
			return
		}
	}
}

// toMap is the shallow map form the module loader interfaces receive.
func (o *OrderedObject) toMap() map[string]any {
	if o == nil {
		return nil
	}
	m := make(map[string]any, o.Len())
	for _, k := range o.keys {
		m[k] = o.values[k]
	}
	return m
}

// remove deletes k in place from an object still under construction.
func (o *OrderedObject) remove(k string) {
	if _, ok := o.values[k]; !ok {
		return
	}
	delete(o.values, k)
	for i, key := range o.keys {
		if key == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// MarshalJSON encodes the object in insertion order with Marshal's jq
// conventions, so encoding/json callers print what jq prints.
func (o *OrderedObject) MarshalJSON() ([]byte, error) { return Marshal(o) }

// FromGo converts Go JSON values to query values: each map[string]any becomes
// an *OrderedObject with its keys in codepoint order, the order a Go map
// cannot state. Arrays without maps are returned as they are.
func FromGo(v any) any {
	out, _ := fromGo(v)
	return out
}

func fromGo(v any) (any, bool) {
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		o := NewObject(len(keys))
		for _, k := range keys {
			x, _ := fromGo(v[k])
			o.Set(k, x)
		}
		return o, true
	case []any:
		var out []any
		for i, x := range v {
			y, changed := fromGo(x)
			if changed && out == nil {
				out = make([]any, len(v))
				copy(out, v)
			}
			if out != nil {
				out[i] = y
			}
		}
		if out == nil {
			return v, false
		}
		return out, true
	}
	return v, false
}

// ToGo converts query values back to Go JSON values: every *OrderedObject
// becomes a map[string]any. Callers that need key order read it before
// converting.
func ToGo(v any) any {
	switch v := v.(type) {
	case *OrderedObject:
		m := make(map[string]any, v.Len())
		for _, k := range v.Keys() {
			m[k] = ToGo(v.values[k])
		}
		return m
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = ToGo(x)
		}
		return out
	}
	return v
}
