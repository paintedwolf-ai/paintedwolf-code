package httpaction

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// wireProvenance names serialized components derived from managed source fields.
// Source screening still sees every literal alongside a reference in those fields.
func wireProvenance(args map[string]any, resolution *secretcap.Resolution, sending outboundRequest) map[int][]string {
	protected := map[int][]string{}
	body := len(sending.headers) + 1
	walkRequestValues(args, "", "", func(path, _, value string) string {
		if !resolution.Binds(path) || !secretmatch.HTTPArgumentConsumed(args, path) {
			return value
		}
		parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
		switch parts[0] {
		case "url":
			protected[0] = append(protected[0], sending.url)
		case "query":
			protected[0] = append(protected[0], url.QueryEscape(value))
		case "headers":
			if i, err := strconv.Atoi(parts[1]); err == nil && parts[2] == "value" {
				protected[i+1] = append(protected[i+1], value)
			}
		case "auth":
			for i, header := range sending.headers {
				if strings.EqualFold(header.Name, "Authorization") {
					protected[i+1] = append(protected[i+1], header.Value)
				}
			}
		case "body_json":
			encoded, _ := json.Marshal(value)
			protected[body] = append(protected[body], string(encoded))
		case "body_text":
			protected[body] = append(protected[body], value)
		case "body_form":
			protected[body] = append(protected[body], url.QueryEscape(value))
		case "form":
			if parts[2] == "name" || parts[2] == "filename" {
				value = escapeQuotes(value)
			}
			protected[body] = append(protected[body], value)
		}
		return value
	})
	return protected
}

func requestWire(spec requestSpec, args map[string]any, resolution *secretcap.Resolution) outboundRequest {
	sending := outboundRequest{url: spec.target.String(), headers: spec.headers, body: spec.body.bytes}
	sending.protected = wireProvenance(args, resolution, sending)
	return sending
}
