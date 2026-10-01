package httpaction

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
)

// scrubResponse rewrites held token values as {{token:name}} and resolved
// secret values as their references before any byte is placed, landed, or
// spilled. The bool reports whether the bytes now differ from the received digest.
func scrubResponse(resp outboundhttp.Response, jar *secretcap.TokenJar, secrets *secretcap.Resolution) (outboundhttp.Response, bool) {
	tokens := tokenScrubber(jar)
	if tokens == nil && !secrets.Resolved() {
		return resp, false
	}
	rewrite := func(text string) string {
		if tokens != nil {
			text = tokens(text)
		}
		return secrets.ReferenceEchoes(text)
	}
	changed := false
	if len(resp.Headers) > 0 {
		headers := make([]outboundhttp.Header, len(resp.Headers))
		for i, header := range resp.Headers {
			value := rewrite(header.Value)
			changed = changed || value != header.Value
			headers[i] = outboundhttp.Header{Name: header.Name, Value: value}
		}
		resp.Headers = headers
	}
	if len(resp.Body) > 0 {
		body := string(resp.Body)
		if scrubbed := rewrite(body); scrubbed != body {
			resp.Body, changed = []byte(scrubbed), true
		}
	}
	return resp, changed
}

// tokenScrubber writes each held token value as its reference, longest value
// first so one token cannot expose part of another. An empty jar needs none.
func tokenScrubber(jar *secretcap.TokenJar) func(string) string {
	if jar == nil || len(jar.Tokens) == 0 {
		return nil
	}
	type held struct{ name, value string }
	var tokens []held
	for name, token := range jar.Tokens {
		if token.Value != "" {
			tokens = append(tokens, held{name, token.Value})
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	sort.Slice(tokens, func(i, j int) bool { return len(tokens[i].value) > len(tokens[j].value) })
	return func(text string) string {
		for _, token := range tokens {
			text = strings.ReplaceAll(text, token.value, "{{token:"+token.name+"}}")
		}
		return text
	}
}
