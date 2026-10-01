package httpaction

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/netip"
	"net/textproto"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

const (
	// maxRequestBody bounds in-memory or form-assembled bodies.
	maxRequestBody = 32 << 20
	// maxResponseFile bounds streamed response bodies written to disk.
	maxResponseFile = 256 << 20
	maxFormParts    = 64
	maxQueryParams  = 64
)

// requestBody is the assembled body and its optional origin path.
type requestBody struct {
	bytes       []byte
	sourcePath  string
	contentType string
}

func parseHeaders(raw any) ([]outboundhttp.Header, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("headers must be an array")
	}
	out := make([]outboundhttp.Header, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("each header must be an object")
		}
		name, nameOK := obj["name"].(string)
		value, valueOK := obj["value"].(string)
		if !nameOK || !valueOK {
			return nil, fmt.Errorf("each header requires string name and value")
		}
		out = append(out, outboundhttp.Header{Name: name, Value: value})
	}
	return out, nil
}

// authHeader encodes credentials after secret substitution.
func authHeader(raw any, headers []outboundhttp.Header) (*outboundhttp.Header, error) {
	if raw == nil {
		return nil, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("auth must be an object")
	}
	if hasHeader(headers, "authorization") {
		return nil, fmt.Errorf("auth and an Authorization header are mutually exclusive")
	}
	scheme, _ := obj["scheme"].(string)
	switch strings.ToLower(strings.TrimSpace(scheme)) {
	case "basic":
		username, _ := obj["username"].(string)
		password, _ := obj["password"].(string)
		if username == "" || strings.Contains(username, ":") {
			return nil, fmt.Errorf("basic auth requires a username without a colon")
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		return &outboundhttp.Header{Name: "Authorization", Value: "Basic " + encoded}, nil
	case "bearer":
		token, _ := obj["token"].(string)
		if strings.TrimSpace(token) == "" {
			return nil, fmt.Errorf("bearer auth requires a token")
		}
		return &outboundhttp.Header{Name: "Authorization", Value: "Bearer " + token}, nil
	default:
		return nil, fmt.Errorf("auth scheme must be basic or bearer")
	}
}

// applyQuery appends parameters without rewriting signed queries or valueless flags.
func applyQuery(target *url.URL, raw any) error {
	if raw == nil {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("query must be an array")
	}
	if len(items) > maxQueryParams {
		return fmt.Errorf("query accepts at most %d parameters", maxQueryParams)
	}
	appended := make([]string, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("each query parameter must be an object")
		}
		name, nameOK := obj["name"].(string)
		value, valueOK := obj["value"].(string)
		if !nameOK || !valueOK || name == "" {
			return fmt.Errorf("each query parameter requires a name and a string value")
		}
		appended = append(appended, url.QueryEscape(name)+"="+url.QueryEscape(value))
	}
	if len(appended) == 0 {
		return nil
	}
	joined := strings.Join(appended, "&")
	if target.RawQuery == "" {
		target.RawQuery = joined
	} else {
		target.RawQuery += "&" + joined
	}
	return nil
}

// bodySourceArgs are the mutually exclusive body arguments.
var bodySourceArgs = []string{"body_text", "body_json", "body_path", "body_form", "form"}

func declaredBodySources(args map[string]any) []string {
	declared := make([]string, 0, len(bodySourceArgs))
	for _, name := range bodySourceArgs {
		if _, present := args[name]; present {
			declared = append(declared, name)
		}
	}
	return declared
}

func hasRequestBody(args map[string]any) bool {
	return len(declaredBodySources(args)) > 0
}

// assembleBody reads the declared body source.
func assembleBody(ctx context.Context, boundary *sandbox.Boundary, tctx tools.ToolContext, args map[string]any) (requestBody, error) {
	declared := declaredBodySources(args)
	if len(declared) > 1 {
		return requestBody{}, fmt.Errorf("%s are mutually exclusive", strings.Join(bodySourceArgs, ", "))
	}
	if len(declared) == 0 {
		return requestBody{}, nil
	}
	var out requestBody
	switch source := declared[0]; source {
	case "body_text":
		text, ok := args[source].(string)
		if !ok {
			return requestBody{}, fmt.Errorf("body_text must be a string")
		}
		out.bytes = []byte(text)
	case "body_json":
		switch args[source].(type) {
		case map[string]any, []any:
		default:
			return requestBody{}, fmt.Errorf("body_json must be a JSON object or array, not serialized JSON text")
		}
		encoded, err := json.Marshal(args[source])
		if err != nil {
			return requestBody{}, err
		}
		out.bytes, out.contentType = encoded, "application/json"
	case "body_path":
		p, ok := args[source].(string)
		if !ok {
			return requestBody{}, fmt.Errorf("body_path must be a project path")
		}
		data, display, err := readProjectFile(ctx, boundary, tctx, p)
		if err != nil {
			return requestBody{}, err
		}
		out.bytes, out.sourcePath = data, display
	case "body_form":
		body, contentType, err := assembleFormURLEncoded(args[source])
		if err != nil {
			return requestBody{}, err
		}
		out.bytes, out.contentType = body, contentType
	case "form":
		body, contentType, sourcePath, err := assembleForm(ctx, boundary, tctx, args[source])
		if err != nil {
			return requestBody{}, err
		}
		out.bytes, out.contentType, out.sourcePath = body, contentType, sourcePath
	}
	if len(out.bytes) > maxRequestBody {
		return requestBody{}, fmt.Errorf("request body exceeds %d bytes", maxRequestBody)
	}
	return out, nil
}

// assembleForm encodes multipart/form-data from ordered parts.
func assembleForm(ctx context.Context, boundary *sandbox.Boundary, tctx tools.ToolContext, raw any) ([]byte, string, string, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, "", "", fmt.Errorf("form must be a non-empty array of parts")
	}
	if len(items) > maxFormParts {
		return nil, "", "", fmt.Errorf("form accepts at most %d parts", maxFormParts)
	}
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	var sourcePath string
	for _, item := range items {
		part, ok := item.(map[string]any)
		if !ok {
			return nil, "", "", fmt.Errorf("each form part must be an object")
		}
		name, _ := part["name"].(string)
		if name == "" {
			return nil, "", "", fmt.Errorf("each form part requires a name")
		}
		value, hasValue := part["value"].(string)
		filePath, hasPath := part["path"].(string)
		if hasValue == hasPath {
			return nil, "", "", fmt.Errorf("form part %q needs exactly one of value or path", name)
		}
		if hasValue {
			if err := writer.WriteField(name, value); err != nil {
				return nil, "", "", err
			}
			if buf.Len() > maxRequestBody {
				return nil, "", "", fmt.Errorf("request body exceeds %d bytes", maxRequestBody)
			}
			continue
		}
		data, display, err := readProjectFile(ctx, boundary, tctx, filePath)
		if err != nil {
			return nil, "", "", err
		}
		sourcePath = display
		filename, _ := part["filename"].(string)
		if filename == "" {
			filename = path.Base(display)
		}
		contentType, _ := part["content_type"].(string)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		header := textproto.MIMEHeader{}
		disposition := fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeQuotes(name), escapeQuotes(filename)) //nolint:gocritic // %q would double-escape what escapeQuotes already did
		header.Set("Content-Disposition", disposition)
		header.Set("Content-Type", contentType)
		dst, err := writer.CreatePart(header)
		if err != nil {
			return nil, "", "", err
		}
		if _, err := dst.Write(data); err != nil {
			return nil, "", "", err
		}
		if buf.Len() > maxRequestBody {
			return nil, "", "", fmt.Errorf("request body exceeds %d bytes", maxRequestBody)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", "", err
	}
	return buf.Bytes(), writer.FormDataContentType(), sourcePath, nil
}

func escapeQuotes(s string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, "\\\"").Replace(s)
}

func readProjectFile(ctx context.Context, boundary *sandbox.Boundary, tctx tools.ToolContext, p string) ([]byte, string, error) {
	files, _ := ctx.Value(requestFilesKey{}).(requestFiles)
	if file, ok := files[p]; ok {
		return file.bytes, file.display, nil
	}
	resolved, err := projectpaths.ResolveRead(ctx, boundary, tctx, p)
	if err != nil {
		return nil, "", err
	}
	info, err := os.Stat(resolved.Abs)
	if err != nil {
		return nil, "", err
	}
	if info.Size() > maxRequestBody {
		return nil, "", fmt.Errorf("request body exceeds %d bytes", maxRequestBody)
	}
	file, err := os.Open(resolved.Abs)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxRequestBody+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxRequestBody {
		return nil, "", fmt.Errorf("request body exceeds %d bytes", maxRequestBody)
	}
	if files != nil {
		files[p] = requestFile{bytes: data, display: resolved.DisplayPath}
	}
	return data, resolved.DisplayPath, nil
}

func hasHeader(headers []outboundhttp.Header, name string) bool {
	for _, header := range headers {
		if strings.EqualFold(strings.TrimSpace(header.Name), name) {
			return true
		}
	}
	return false
}

func durationArg(raw any) time.Duration {
	switch value := raw.(type) {
	case float64:
		return time.Duration(value) * time.Millisecond
	case int:
		return time.Duration(value) * time.Millisecond
	case int64:
		return time.Duration(value) * time.Millisecond
	default:
		return outboundhttp.DefaultTimeout
	}
}

// responseDisposition specifies how the response body is delivered.
type responseDisposition struct {
	discard bool
	path    string
}

func parseResponseDisposition(args map[string]any) (responseDisposition, error) {
	mode, _ := args["response_body"].(string)
	p, _ := args["response_path"].(string)
	p = strings.TrimSpace(p)
	if _, present := args["response_path"]; present && p == "" {
		return responseDisposition{}, fmt.Errorf("response_path must name a project path")
	}
	if p != "" && mode == "discard" {
		return responseDisposition{}, fmt.Errorf("response_path and response_body: discard are mutually exclusive")
	}
	return responseDisposition{discard: mode == "discard", path: p}, nil
}

// assembleFormURLEncoded encodes application/x-www-form-urlencoded from ordered fields.
func assembleFormURLEncoded(raw any) ([]byte, string, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, "", fmt.Errorf("body_form must be a non-empty array of fields")
	}
	if len(items) > maxQueryParams {
		return nil, "", fmt.Errorf("body_form accepts at most %d fields", maxQueryParams)
	}
	appended := make([]string, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("each body_form field must be an object")
		}
		name, nameOK := obj["name"].(string)
		value, valueOK := obj["value"].(string)
		if !nameOK || !valueOK || name == "" {
			return nil, "", fmt.Errorf("each body_form field requires a name and a string value")
		}
		appended = append(appended, url.QueryEscape(name)+"="+url.QueryEscape(value))
	}
	return []byte(strings.Join(appended, "&")), "application/x-www-form-urlencoded", nil
}

// parseUnixSocket validates the stated socket; the executor reviews it as
// exact socket authority before the handler runs.
func parseUnixSocket(raw any) (string, error) {
	if raw == nil {
		return "", nil
	}
	p, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("unix_socket must be a string")
	}
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("unix_socket must name a socket path")
	}
	return p, nil
}

func parseResolve(raw any) ([]outboundhttp.ResolveMapping, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("resolve must be an array")
	}
	out := make([]outboundhttp.ResolveMapping, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("each resolve item must be an object")
		}
		host, _ := obj["host"].(string)
		host = strings.TrimSpace(host)
		if host == "" {
			return nil, fmt.Errorf("each resolve item requires a host")
		}
		ipStr, _ := obj["ip"].(string)
		ipStr = strings.TrimSpace(ipStr)
		if ipStr == "" {
			return nil, fmt.Errorf("each resolve item requires an ip")
		}
		addr, err := netip.ParseAddr(ipStr)
		if err != nil {
			return nil, fmt.Errorf("resolve ip %q is invalid: %w", ipStr, err)
		}
		var port uint16
		if rawPort, ok := obj["port"]; ok && rawPort != nil {
			switch p := rawPort.(type) {
			case float64:
				if p < 1 || p > 65535 {
					return nil, fmt.Errorf("resolve port %v is out of range", p)
				}
				port = uint16(p)
			case int:
				if p < 1 || p > 65535 {
					return nil, fmt.Errorf("resolve port %v is out of range", p)
				}
				port = uint16(p)
			default:
				return nil, fmt.Errorf("resolve port must be an integer")
			}
		}
		out = append(out, outboundhttp.ResolveMapping{
			Host:    host,
			Address: addr,
			Port:    port,
		})
	}
	return out, nil
}

func parseHostHeader(raw any) (string, error) {
	if raw == nil {
		return "", nil
	}
	h, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("host_header must be a string")
	}
	h = strings.TrimSpace(h)
	return h, nil
}

