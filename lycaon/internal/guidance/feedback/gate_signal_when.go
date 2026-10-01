package feedback

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/lycaon/lycaon/internal/boolexpr"
)

// Gate expressions read context keys through the shared boolean grammar.
const (
	gateSignalTrue  = "true"
	gateSignalFalse = "false"
)

// parseGateSignalWhen compiles one optional expression.
func parseGateSignalWhen(when string) (boolexpr.Node, error) {
	when = strings.TrimSpace(when)
	if when == "" {
		return nil, nil
	}
	node, err := boolexpr.Parse(when)
	if err != nil {
		return nil, fmt.Errorf("when %q: %w", when, err)
	}
	if err := boolexpr.ValidateWhitelist(node); err != nil {
		return nil, fmt.Errorf("when %q: %w", when, err)
	}
	// Gate context is a fixed map, not a registry: an unrecognized name would read
	// as missing and evaluate false, so template syntax must fail at load instead.
	if err := boolexpr.ValidateSimpleIdents(node); err != nil {
		return nil, fmt.Errorf("when %q: %w", when, err)
	}
	return node, nil
}

// evalGateSignalWhen evaluates an optional gate expression.
func evalGateSignalWhen(node boolexpr.Node, ctx map[string]any) bool {
	if node == nil {
		return true
	}
	return boolexpr.Eval(node, func(name string) bool {
		switch name {
		case gateSignalTrue:
			return true
		case gateSignalFalse:
			return false
		}
		value, ok := ctx[name]
		if !ok {
			return false
		}
		return gateContextTruthy(value)
	})
}

// gateContextTruthy reads one context value as a condition. The context carries
// strings, bools, and string slices; emptiness is the falsy case for each.
func gateContextTruthy(value any) bool {
	if value == nil {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.TrimSpace(v) != ""
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return rv.Len() > 0
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int() != 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint() != 0
	case reflect.Float32, reflect.Float64:
		return rv.Float() != 0
	case reflect.Pointer, reflect.Interface:
		return !rv.IsNil()
	default:
		return true
	}
}
