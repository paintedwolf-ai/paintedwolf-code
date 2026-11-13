package oarcore

import (
	"encoding/json"
	"math"
	"strconv"
)

func coerce(raw any, typ FactType, name string) (value, error) {
	switch typ {
	case TypeBool:
		b, ok := raw.(bool)
		if !ok {
			return nil, raise("[OAR-FACT-18] fact %s is not a bool", name)
		}
		return b, nil
	case TypeInt:
		switch n := raw.(type) {
		case int64:
			return n, nil
		case int:
			return int64(n), nil
		case float64:
			if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n >= 9223372036854775808.0 || n < -9223372036854775808.0 {
				return nil, raise("[OAR-FACT-18] fact %s is not an int", name)
			}
			return int64(n), nil
		case json.Number:
			v, err := strconv.ParseInt(string(n), 10, 64)
			if err != nil {
				return nil, raise("[OAR-FACT-18] fact %s is not an int", name)
			}
			return v, nil
		default:
			return nil, raise("[OAR-FACT-18] fact %s is not an int", name)
		}
	case TypeDouble:
		n, ok := raw.(float64)
		if !ok {
			return nil, raise("[OAR-FACT-18] fact %s is not a double", name)
		}
		// Double facts must be finite ([OAR-FACT-23]).
		if math.IsInf(n, 0) || math.IsNaN(n) {
			return nil, raise("[OAR-FACT-18] fact %s is not a finite double", name)
		}
		return n, nil
	case TypeString:
		s, ok := raw.(string)
		if !ok {
			return nil, raise("[OAR-FACT-18] fact %s is not a string", name)
		}
		return s, nil
	case TypeListString:
		switch t := raw.(type) {
		case []string:
			return t, nil
		case []any:
			out := make([]string, len(t))
			for i, x := range t {
				s, ok := x.(string)
				if !ok {
					return nil, raise("[OAR-FACT-18] fact %s is not a list<string>", name)
				}
				out[i] = s
			}
			return out, nil
		default:
			return nil, raise("[OAR-FACT-18] fact %s is not a list<string>", name)
		}
	case TypeListMap:
		switch t := raw.(type) {
		case []map[string]any:
			return t, nil
		case []any:
			out := make([]map[string]any, len(t))
			for i, x := range t {
				m, ok := asObject(x)
				if !ok {
					return nil, raise("[OAR-FACT-18] fact %s is not a list<map<string, dyn>>", name)
				}
				out[i] = m
			}
			return out, nil
		default:
			return nil, raise("[OAR-FACT-18] fact %s is not a list<map<string, dyn>>", name)
		}
	case TypeMap:
		m, ok := asObject(raw)
		if !ok {
			return nil, raise("[OAR-FACT-18] fact %s is not a map<string, dyn>", name)
		}
		return m, nil
	default:
		return nil, raise("[OAR-FACT-18] fact %s has an undeclarable type", name)
	}
}
