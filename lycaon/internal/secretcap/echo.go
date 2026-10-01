package secretcap

import (
	"bytes"
	"encoding/base64"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// base64Run is a candidate encoded span; eight characters encode the shortest admitted value.
var base64Run = regexp.MustCompile(`[A-Za-z0-9+/_-]{8,}={0,2}`)

// ReferenceEchoes rewrites echoed secret values, plain and in every serialized
// spelling secretmatch.EncodedForms lists, back to their secret references. A
// base64 span that embeds a value among other bytes, such as a Basic
// credential, is masked whole.
func (r *Resolution) ReferenceEchoes(text string) string {
	if r == nil || len(r.values) == 0 || text == "" {
		return text
	}
	text = base64Run.ReplaceAllStringFunc(text, r.decodedEcho)
	return r.ReferenceValues(text)
}

// decodedEcho also tries the run past a glued prefix of up to three
// characters, such as the n of an escaped newline in serialized output.
func (r *Resolution) decodedEcho(run string) string {
	for offset := 0; offset < 4 && len(run)-offset >= 8; offset++ {
		decoded, ok := decodeBase64Run(run[offset:])
		if !ok {
			continue
		}
		for id, v := range r.values {
			if v.value == "" {
				continue
			}
			if string(decoded) == v.value {
				return run[:offset] + secretmatch.ReferenceToken(id)
			}
			if bytes.Contains(decoded, []byte(v.value)) {
				return run[:offset] + executionRedaction
			}
		}
	}
	return run
}

func decodeBase64Run(run string) ([]byte, bool) {
	raw := strings.TrimRight(run, "=")
	encoding := base64.RawStdEncoding
	if strings.ContainsAny(raw, "-_") {
		if strings.ContainsAny(raw, "+/") {
			return nil, false
		}
		encoding = base64.RawURLEncoding
	}
	decoded, err := encoding.DecodeString(raw)
	return decoded, err == nil
}

// ReferenceEchoesIn applies ReferenceEchoes to every string in structured data.
func (r *Resolution) ReferenceEchoesIn(value any) any {
	if r == nil || len(r.values) == 0 {
		return value
	}
	switch v := value.(type) {
	case string:
		return r.ReferenceEchoes(v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			out[key] = r.ReferenceEchoesIn(child)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = r.ReferenceEchoesIn(child)
		}
		return out
	case []string:
		out := make([]string, len(v))
		for i, child := range v {
			out[i] = r.ReferenceEchoes(child)
		}
		return out
	default:
		return v
	}
}
