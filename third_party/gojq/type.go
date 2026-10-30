package gojq

import (
	"encoding/json"
	"fmt"
	"math/big"
)

// TypeOf returns the jq-flavored type name of v.
//
// This method is used by built-in type/0 function, and accepts only limited
// types (nil, bool, int, float64, *big.Int, json.Number, string, []any, and
// *OrderedObject).
func TypeOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case int, float64, *big.Int, json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case *OrderedObject:
		return "object"
	default:
		panic(fmt.Sprintf("invalid type: %[1]T (%[1]v)", v))
	}
}
