package logoutline

import (
	"strconv"
)

var (
	timeKeys    = []string{"ts", "time", "@timestamp", "timestamp"}
	levelKeys   = []string{"level", "lvl", "severity"}
	messageKeys = []string{"msg", "message"}
)

func pickField(fields map[string]string, keys []string) string {
	for _, k := range keys {
		if v, ok := fields[k]; ok && v != "" {
			return v
		}
	}
	return ""
}

func stringFields(m map[string]any) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if v == nil {
			continue
		}
		out[k] = stringifyJSONValue(v)
	}
	return out
}

func stringifyJSONValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return ""
	}
}
