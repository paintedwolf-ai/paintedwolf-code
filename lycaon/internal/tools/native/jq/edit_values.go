package jq

import (
	"encoding/json"
	"math"
	"math/big"
	"sort"
)

// keyOrder records source key order by structural position. Every element of
// an array shares one position, so filtered or reordered arrays keep it.
type keyOrder struct {
	keys map[string][]string
	seen map[string]map[string]struct{}
	held map[string]struct{}
}

func newKeyOrder() *keyOrder {
	return &keyOrder{keys: map[string][]string{}, seen: map[string]map[string]struct{}{}, held: map[string]struct{}{}}
}

// known reports whether the source held a value at pos.
func (o *keyOrder) known(pos string) bool {
	_, ok := o.held[pos]
	return ok
}

const rootPos = ""

func childPos(pos, key string) string { return pos + "\x1f" + key }

func elemPos(pos string) string { return pos + "\x1f[]" }

func (o *keyOrder) note(pos, key string) {
	o.held[childPos(pos, key)] = struct{}{}
	seen := o.seen[pos]
	if seen == nil {
		seen = map[string]struct{}{}
		o.seen[pos] = seen
	}
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	o.keys[pos] = append(o.keys[pos], key)
}

// ordered lists m's keys in source order; keys the source never held follow,
// sorted.
func (o *keyOrder) ordered(pos string, m map[string]any) []string {
	out := make([]string, 0, len(m))
	placed := make(map[string]struct{}, len(m))
	for _, k := range o.keys[pos] {
		if _, ok := m[k]; ok {
			out = append(out, k)
			placed[k] = struct{}{}
		}
	}
	rest := make([]string, 0, len(m)-len(out))
	for k := range m {
		if _, ok := placed[k]; !ok {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// sameValue compares query values the way jq does: numbers by value.
func sameValue(a, b any) bool {
	switch av := a.(type) {
	case nil:
		return b == nil
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !sameValue(av[i], bv[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			w, ok := bv[k]
			if !ok || !sameValue(v, w) {
				return false
			}
		}
		return true
	}
	ar, aok := numberRat(a)
	br, bok := numberRat(b)
	return aok && bok && ar.Cmp(br) == 0
}

func numberRat(v any) (*big.Rat, bool) {
	switch n := v.(type) {
	case int:
		return new(big.Rat).SetInt64(int64(n)), true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, false
		}
		return new(big.Rat).SetFloat64(n), true
	case *big.Int:
		return new(big.Rat).SetInt(n), true
	case json.Number:
		return new(big.Rat).SetString(n.String())
	}
	return nil, false
}
