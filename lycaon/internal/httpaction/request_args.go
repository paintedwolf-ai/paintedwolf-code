package httpaction

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"mime/multipart"
	"net/textproto"
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
	maxFormParts   = 64
	maxQueryParams = 64
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

// bodySourceArgs are the mutually exclusive body arguments.
var bodySourceArgs = []string{"body_text", "form"}

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
	data, err := resolved.ReadBounded(maxRequestBody)
	var tooLarge *projectpaths.FileTooLargeError
	if errors.As(err, &tooLarge) {
		return nil, "", fmt.Errorf("request body exceeds %d bytes", maxRequestBody)
	}
	if err != nil {
		return nil, "", err
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
