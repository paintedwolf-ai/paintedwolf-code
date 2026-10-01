package messageview

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/pkg/api"
)

// Argument spans map decoded field positions through JSON encoding.
func argumentContent(args map[string]any, prefix string, meta *api.HostSecretRedactionMeta) (string, []api.RedactedSpan, error) {
	body, err := json.Marshal(args)
	if err != nil {
		return "", nil, err
	}
	spans := encodedArgumentSpans(body, prefix, meta.SpanList(), 0)
	text, displayed := readableContent(string(body), spans)
	return text, displayed, nil
}

func encodedArgumentSpans(body []byte, path string, spans []api.RedactedSpan, base int) []api.RedactedSpan {
	if len(body) == 0 {
		return nil
	}
	if body[0] == '"' {
		var value string
		if json.Unmarshal(body, &value) != nil {
			return nil
		}
		runes := []rune(value)
		var out []api.RedactedSpan
		for _, span := range spans {
			if span.Field != path || span.Start < 0 || span.Start > len(runes) {
				continue
			}
			before, beforeErr := json.Marshal(string(runes[:span.Start]))
			selected, selectedErr := json.Marshal(string(runes[span.Start:min(len(runes), span.Start+span.Length)]))
			if beforeErr != nil || selectedErr != nil {
				continue
			}
			span.Start = base + 1 + utf8.RuneCount(before) - 2
			span.Length = utf8.RuneCount(selected) - 2
			out = append(out, span)
		}
		return out
	}
	if body[0] != '{' && body[0] != '[' {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if _, err := decoder.Token(); err != nil {
		return nil
	}
	var out []api.RedactedSpan
	for index := 0; decoder.More(); index++ {
		key := strconv.Itoa(index)
		if body[0] == '{' {
			token, err := decoder.Token()
			if err != nil {
				break
			}
			key, _ = token.(string)
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			break
		}
		start := int(decoder.InputOffset()) - len(raw)
		child := strings.TrimPrefix(path+"."+key, ".")
		out = append(out, encodedArgumentSpans(raw, child, spans, base+utf8.RuneCount(body[:start]))...)
	}
	return out
}
