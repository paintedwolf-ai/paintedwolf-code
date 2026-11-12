package curl

import (
	"maps"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/toolrejection"
)

// secretSlots are the request placements where a managed-secret reference
// reaches the recipient the http_request owner admits, and the passphrase
// that opens a client key. Every other curl value (the URL, file paths, -A,
// cookies, -G and URL-encoded data) is not a sink, so a reference there
// refuses.
var secretSlots = []string{"headers.[].value", "host_header", "auth.username", "auth.password", "body_text", "form.[].value", "tls.passphrase"}

// passphrasePath is where the record carries --pass.
const passphrasePath = "/tls/passphrase"

// SecretSlots names curl's reference slots for its program definition.
func (o *Owners) SecretSlots() []string { return slices.Clone(secretSlots) }

// requestRecord is the part of the http_request record curl's options fix
// before anything is read: user headers, Host, basic credentials, an inline
// body and form parts. Its paths are the paths requestArgs builds, so a value
// resolved in the record is the value the request sends. represented names
// the options the record stands for.
func requestRecord(o options) (record map[string]any, represented map[string]bool) {
	record, represented = map[string]any{}, map[string]bool{}
	if len(o.headers) > 0 {
		headers := []any{}
		for _, h := range o.headers {
			if strings.EqualFold(h[0], "Host") {
				record["host_header"] = h[1]
				continue
			}
			headers = append(headers, map[string]any{"name": h[0], "value": h[1]})
		}
		if len(headers) > 0 {
			record["headers"] = headers
		}
		represented["-H"] = true
	}
	if name, password, ok := strings.Cut(o.user, ":"); ok {
		record["auth"] = map[string]any{"scheme": "basic", "username": name, "password": password}
		represented["-u"] = true
	}
	if o.unixSocket != "" {
		// The executor reviews this argument as the request's local service socket.
		record["unix_socket"] = o.unixSocket
		represented["--unix-socket"] = true
	}
	if o.tls.pass != "" {
		record["tls"] = map[string]any{"passphrase": o.tls.pass}
		represented["--pass"] = true
	}
	switch {
	case o.upload != "" || o.get:
	case len(o.forms) > 0:
		parts := make([]any, 0, len(o.forms))
		for _, form := range o.forms {
			name, value, _ := strings.Cut(form, "=")
			if strings.HasPrefix(value, "@") || strings.HasPrefix(value, "<") {
				parts = append(parts, map[string]any{"name": name, "file": value})
			} else {
				parts = append(parts, map[string]any{"name": name, "value": value})
			}
		}
		record["form"] = parts
		represented["-F"] = true
	case len(o.json) > 0:
		if body, ok := inlineJSON(o.json); ok {
			record["body_text"] = body
			represented["--json"] = true
		}
	case len(o.data) > 0:
		if body, ok := inlineData(o.data); ok {
			record["body_text"] = body
			for _, key := range []string{"-d", "--data-raw", "--data-binary"} {
				represented[key] = true
			}
		}
	}
	return record, represented
}

// inlineJSON is the --json body when no value is read from a file.
func inlineJSON(values []string) (string, bool) {
	for _, value := range values {
		if strings.HasPrefix(value, "@") {
			return "", false
		}
	}
	return strings.Join(values, ""), true
}

// inlineData is the data body when every part is sent as written: no file is
// read and nothing is URL-encoded, so the sent body is the record's text.
func inlineData(parts []dataPart) (string, bool) {
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.kind == "urlencode" || part.kind != "raw" && strings.HasPrefix(part.value, "@") {
			return "", false
		}
		values = append(values, part.value)
	}
	return strings.Join(values, "&"), true
}

// applyResolved writes the record's resolved values over the request built
// from options, at the record's own paths.
func applyResolved(args, record, resolved map[string]any) {
	if resolved == nil {
		return
	}
	if want, ok := record["headers"].([]any); ok {
		headers, _ := args["headers"].([]any)
		got, _ := resolved["headers"].([]any)
		for i := range want {
			if i < len(headers) && i < len(got) {
				header, _ := headers[i].(map[string]any)
				value, _ := got[i].(map[string]any)
				if header != nil && value != nil {
					header["value"] = value["value"]
				}
			}
		}
	}
	for _, key := range []string{"host_header", "body_text"} {
		if _, ok := record[key]; ok {
			args[key] = resolved[key]
		}
	}
	if _, ok := record["auth"]; ok {
		if auth, ok := resolved["auth"].(map[string]any); ok {
			args["auth"] = maps.Clone(auth)
		}
	}
	if want, ok := record["form"].([]any); ok {
		parts, _ := args["form"].([]any)
		got, _ := resolved["form"].([]any)
		for i := range want {
			inline, _ := want[i].(map[string]any)
			if _, ok := inline["value"]; !ok || i >= len(parts) || i >= len(got) {
				continue
			}
			part, _ := parts[i].(map[string]any)
			value, _ := got[i].(map[string]any)
			if part != nil && value != nil {
				part["value"] = value["value"]
			}
		}
	}
}

// resolvedRecord is the admitted record after resolution, or as written when
// the invocation resolved nothing.
func (inv *invocation) resolvedRecord() map[string]any {
	if inv.tc.Effects.Secrets != nil && inv.tc.Effects.Secrets.Arguments != nil {
		return inv.tc.Effects.Secrets.Arguments
	}
	return inv.record
}

// passphrase is the resolved --pass, released under the same rules as any
// value leaving its resolution, since it opens the key the request presents.
func (inv *invocation) passphrase() (string, error) {
	if _, ok := inv.record["tls"]; !ok {
		return "", nil
	}
	tls, _ := inv.resolvedRecord()["tls"].(map[string]any)
	value, _ := tls["passphrase"].(string)
	if err := inv.tc.Effects.Secrets.HandOff(inv.ctx, func(path string) bool { return path == passphrasePath }); err != nil {
		return "", toolrejection.HeldHandOffReject("http_request", err)
	}
	return value, nil
}
