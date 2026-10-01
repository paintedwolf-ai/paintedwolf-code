package secretmatch

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
)

// EncodedForms lists the spellings a serializer gives value on its way into a
// transcript, request, or file: percent-encoded for a URL, escaped inside a
// JSON string with and without HTML-safe escaping, each of those inside one
// further JSON string, and base64 of the whole value. A form equal to value
// or to an earlier form is omitted.
func EncodedForms(value string) []string {
	if value == "" {
		return nil
	}
	seen := map[string]struct{}{value: {}}
	var forms []string
	add := func(form string) {
		if _, dup := seen[form]; dup {
			return
		}
		seen[form] = struct{}{}
		forms = append(forms, form)
	}
	nested := []func(string) string{jsonEscaped(true), jsonEscaped(false)}
	for _, serialize := range []func(string) string{url.QueryEscape, url.PathEscape, jsonEscaped(true), jsonEscaped(false)} {
		encoded := serialize(value)
		add(encoded)
		for _, again := range nested {
			add(again(encoded))
		}
	}
	raw := []byte(value)
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		add(encoding.EncodeToString(raw))
	}
	return forms
}

// jsonEscaped spells value as the body of a JSON string literal.
func jsonEscaped(escapeHTML bool) func(string) string {
	return func(value string) string {
		var buf bytes.Buffer
		encoder := json.NewEncoder(&buf)
		encoder.SetEscapeHTML(escapeHTML)
		if err := encoder.Encode(value); err != nil {
			return value
		}
		return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSuffix(buf.String(), "\n"), `"`), `"`)
	}
}
