package oarcore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CanonicalDouble is the finite binary64 rendering defined by [OAR-COPY-10].
func CanonicalDouble(v float64) string {
	if v == 0 {
		return "0.0"
	}
	text := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.Contains(text, ".") {
		text += ".0"
	}
	return text
}

// ToolFingerprint binds both tool identity and arguments ([OAR-FACT-28]).
func ToolFingerprint(name string, arguments map[string]any) (string, error) {
	var out strings.Builder
	out.WriteString("oar-tool-1.0\x00")
	if err := canonicalJSON(&out, []any{name, arguments}); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(out.String()))
	return hex.EncodeToString(digest[:]), nil
}

func canonicalJSON(out *strings.Builder, raw any) error {
	switch v := raw.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(v))
	case string:
		if !utf8.ValidString(v) {
			return fmt.Errorf("[OAR-FACT-28] invalid Unicode string")
		}
		out.WriteByte('"')
		for _, r := range v {
			switch {
			case r == '"' || r == '\\':
				out.WriteByte('\\')
				out.WriteRune(r)
			case r < 32:
				fmt.Fprintf(out, "\\u%04x", r)
			default:
				out.WriteRune(r)
			}
		}
		out.WriteByte('"')
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("[OAR-FACT-28] non-finite number")
		}
		out.WriteString(CanonicalDouble(v))
	case int:
		return canonicalJSON(out, int64(v))
	case int64:
		f := float64(v)
		if f >= 9223372036854775808.0 || int64(f) != v {
			return fmt.Errorf("[OAR-FACT-28] integer loses precision")
		}
		return canonicalJSON(out, f)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return canonicalJSON(out, n)
		}
		f, err := v.Float64()
		if err != nil {
			return fmt.Errorf("[OAR-FACT-28] invalid number")
		}
		return canonicalJSON(out, f)
	case []string:
		items := make([]any, len(v))
		for i, item := range v {
			items[i] = item
		}
		return canonicalJSON(out, items)
	case map[string]string:
		members := make(map[string]any, len(v))
		for key, value := range v {
			members[key] = value
		}
		return canonicalJSON(out, members)
	case []any:
		out.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := canonicalJSON(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := canonicalJSON(out, key); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := canonicalJSON(out, v[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("[OAR-FACT-28] unsupported JSON value %T", raw)
	}
	return nil
}
