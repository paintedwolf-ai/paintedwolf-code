package curl

import (
	"io"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/toolrejection"
)

// requestArgs builds the http_request owner's declared argument record from
// curl's forms. Headers, credentials and bodies are carried as written, so a
// secret reference reaches only the owner's declared header and auth sinks.
func (inv *invocation) requestArgs(cookieHeader string) (map[string]any, error) {
	o := inv.opts
	args := map[string]any{"url": o.url}
	method := o.method
	headers := make([]any, 0, len(o.headers)+2)
	set := map[string]bool{}
	for _, h := range o.headers {
		set[strings.ToLower(h[0])] = true
		headers = append(headers, map[string]any{"name": h[0], "value": h[1]})
	}
	addDefault := func(name, value string) {
		if !set[strings.ToLower(name)] {
			headers = append(headers, map[string]any{"name": name, "value": value})
		}
	}
	if o.userAgent != "" {
		addDefault("User-Agent", o.userAgent)
	}
	if o.compressed {
		addDefault("Accept-Encoding", "deflate, gzip")
	}
	if cookieHeader != "" {
		headers = append(headers, map[string]any{"name": "Cookie", "value": cookieHeader})
	}
	if o.byteRange != "" {
		addDefault("Range", "bytes="+o.byteRange)
	}
	switch {
	case o.head:
		if method == "" {
			method = "HEAD"
		}
	case o.upload != "":
		body, err := inv.uploadBody()
		if err != nil {
			return nil, err
		}
		args["body_text"] = body
		args["url"] = uploadURL(o.url, o.upload)
		if method == "" {
			method = "PUT"
		}
	case len(o.forms) > 0:
		parts, err := inv.formParts()
		if err != nil {
			return nil, err
		}
		args["form"] = parts
		if method == "" {
			method = "POST"
		}
	case o.get && len(o.data) > 0:
		query, err := inv.dataBody()
		if err != nil {
			return nil, err
		}
		args["url"] = withQuery(o.url, query)
	case len(o.json) > 0:
		body, err := inv.jsonBody()
		if err != nil {
			return nil, err
		}
		args["body_text"] = body
		addDefault("Content-Type", "application/json")
		addDefault("Accept", "application/json")
		if method == "" {
			method = "POST"
		}
	case len(o.data) > 0:
		body, err := inv.dataBody()
		if err != nil {
			return nil, err
		}
		args["body_text"] = body
		addDefault("Content-Type", "application/x-www-form-urlencoded")
		if method == "" {
			method = "POST"
		}
	}
	if method != "" {
		args["method"] = method
	}
	if len(headers) > 0 {
		args["headers"] = headers
	}
	if o.user != "" {
		name, password, ok := strings.Cut(o.user, ":")
		if !ok {
			return nil, usage(o.user, "USER:PASSWORD; curl does not prompt")
		}
		args["auth"] = map[string]any{"scheme": "basic", "username": name, "password": password}
	}
	if o.location {
		args["redirects"] = "safe"
	} else {
		args["redirects"] = "none"
	}
	if o.maxTime > 0 {
		args["timeout_ms"] = float64(o.maxTime.Milliseconds())
	}
	if o.unixSocket != "" {
		bound, err := inv.program.Paths.Bind(inv.ctx, o.unixSocket, false)
		if err != nil {
			return nil, err
		}
		args["unix_socket"] = bound.Resolved.Abs
	}
	return args, nil
}

// readFile reads an @file operand through the invocation's read authority.
func (inv *invocation) readFile(operand string) ([]byte, error) {
	bound, err := inv.program.Paths.Bind(inv.ctx, operand, false)
	if err != nil {
		return nil, err
	}
	f, err := bound.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	maximum := inv.owners.limits["stream_bytes"]
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": "stream_bytes", "resource_unit": "bytes", "maximum": maximum, "observed": int64(len(data))}}
	}
	return data, nil
}

// uploadBody is -T's file read through read authority, or stdin for "-",
// as exact bytes.
func (inv *invocation) uploadBody() (string, error) {
	if inv.opts.upload != "-" {
		data, err := inv.readFile(inv.opts.upload)
		return string(data), err
	}
	maximum := inv.owners.limits["stream_bytes"]
	data, err := io.ReadAll(io.LimitReader(inv.stdin, maximum+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maximum {
		return "", &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": "stream_bytes", "resource_unit": "bytes", "maximum": maximum, "observed": int64(len(data))}}
	}
	return string(data), nil
}

// uploadURL appends the uploaded file's name to a URL without a file part,
// as curl does; stdin uploads keep the URL.
func uploadURL(raw, upload string) string {
	u, err := url.Parse(raw)
	if err != nil || upload == "-" || u.Path != "" && !strings.HasSuffix(u.Path, "/") {
		return raw
	}
	if u.Path == "" {
		u.Path = "/"
	}
	u.Path += path.Base(filepath.ToSlash(upload))
	return u.String()
}

// dataBody joins the data parts with '&'. -d @file drops CR and LF as curl
// does; --data-binary @file keeps the bytes; --data-raw never reads a file.
func (inv *invocation) dataBody() (string, error) {
	parts := make([]string, 0, len(inv.opts.data))
	for _, part := range inv.opts.data {
		value := part.value
		switch part.kind {
		case "data", "binary":
			if file, ok := strings.CutPrefix(value, "@"); ok {
				data, err := inv.readFile(file)
				if err != nil {
					return "", err
				}
				value = string(data)
				if part.kind == "data" {
					value = strings.NewReplacer("\r", "", "\n", "").Replace(value)
				}
			}
		case "urlencode":
			encoded, err := inv.urlencoded(value)
			if err != nil {
				return "", err
			}
			value = encoded
		}
		parts = append(parts, value)
	}
	return strings.Join(parts, "&"), nil
}

// jsonBody concatenates --json values without a separator, as curl does;
// @file values are read as exact bytes.
func (inv *invocation) jsonBody() (string, error) {
	var b strings.Builder
	for _, value := range inv.opts.json {
		if file, ok := strings.CutPrefix(value, "@"); ok {
			data, err := inv.readFile(file)
			if err != nil {
				return "", err
			}
			value = string(data)
		}
		b.WriteString(value)
	}
	return b.String(), nil
}

// withQuery appends -G data to the URL's query, before any fragment.
func withQuery(raw, query string) string {
	base, fragment, hasFragment := strings.Cut(raw, "#")
	separator := "?"
	if strings.Contains(base, "?") {
		separator = "&"
	}
	out := base + separator + query
	if hasFragment {
		out += "#" + fragment
	}
	return out
}

// urlencoded applies curl's --data-urlencode forms: content, =content,
// name=content, @file and name@file.
func (inv *invocation) urlencoded(value string) (string, error) {
	eq := strings.IndexByte(value, '=')
	at := strings.IndexByte(value, '@')
	if at >= 0 && (eq < 0 || at < eq) {
		data, err := inv.readFile(value[at+1:])
		if err != nil {
			return "", err
		}
		encoded := url.QueryEscape(string(data))
		if at == 0 {
			return encoded, nil
		}
		return value[:at] + "=" + encoded, nil
	}
	switch {
	case eq == 0:
		return url.QueryEscape(value[1:]), nil
	case eq > 0:
		return value[:eq] + "=" + url.QueryEscape(value[eq+1:]), nil
	}
	return url.QueryEscape(value), nil
}

// formParts maps -F name=value, name=@path[;type=T][;filename=F] and
// name=<path to the owner's multipart parts; @path is read by the owner.
func (inv *invocation) formParts() ([]any, error) {
	parts := make([]any, 0, len(inv.opts.forms))
	for _, form := range inv.opts.forms {
		name, value, ok := strings.Cut(form, "=")
		if !ok || name == "" {
			return nil, usage(form, "a form part as name=value, name=@file or name=<file")
		}
		switch {
		case strings.HasPrefix(value, "@"):
			fields := strings.Split(value[1:], ";")
			bound, err := inv.program.Paths.Bind(inv.ctx, fields[0], false)
			if err != nil {
				return nil, err
			}
			part := map[string]any{"name": name, "path": bound.Resolved.Abs}
			for _, field := range fields[1:] {
				key, v, _ := strings.Cut(field, "=")
				switch key {
				case "type":
					part["content_type"] = v
				case "filename":
					part["filename"] = v
				default:
					return nil, usage(field, "type= or filename= form part attributes")
				}
			}
			parts = append(parts, part)
		case strings.HasPrefix(value, "<"):
			data, err := inv.readFile(value[1:])
			if err != nil {
				return nil, err
			}
			parts = append(parts, map[string]any{"name": name, "value": string(data)})
		default:
			parts = append(parts, map[string]any{"name": name, "value": value})
		}
	}
	return parts, nil
}
