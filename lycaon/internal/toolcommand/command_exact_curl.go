package toolcommand

import (
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/outboundhttp"
)

// curlRequest accumulates mapped arguments while scanning an invocation.
type curlRequest struct {
	target         string
	method         string
	methodExplicit bool
	head           bool
	headers        []outboundhttp.Header
	contentType    bool
	dataParts      []string
	bodyText       string
	bodyPath       string
	bodySet        bool
	jsonBody       bool
	follow         bool
	timeoutMS      int
	discardBody    bool
	responsePath   string
	remoteName     bool
	cookieJar      string
	cookieHeader   string
	basicUser      string
	basicPassword  string
	basicAuth      bool
	formParts      []map[string]any
	urlencoded     []string
	unixSocket     string
}

// curlBooleanShortFlags are single-letter flags that take no value and may be bundled.
const curlBooleanShortFlags = "sSLivIg"

// curlPassthroughFlags are reporting flags whose output is covered by the structured result.
var curlPassthroughFlags = map[string]bool{
	"-s": true, "--silent": true, "-S": true, "--show-error": true,
	"-g": true, "--globoff": true,
	"-i": true, "--include": true,
	"-v": true, "--verbose": true,
	"--compressed": true, "--no-progress-meter": true, "-#": true, "--progress-bar": true,
}

var curlWriteOutSubstitution = regexp.MustCompile(`%\{([^}]*)\}`)

// curlReplacement translates an argv into an http_request call.
func curlReplacement(args []string) (ReplacementCall, bool) {
	args, ok := expandCurlShortFlags(args)
	if !ok {
		return ReplacementCall{}, false
	}
	req := curlRequest{method: "GET"}
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if curlPassthroughFlags[flag] {
			continue
		}
		if !strings.HasPrefix(flag, "-") {
			if req.target != "" {
				return ReplacementCall{}, false
			}
			req.target = flag
			continue
		}
		switch flag {
		case "-L", "--location":
			req.follow = true
		case "-I", "--head":
			if req.methodExplicit {
				return ReplacementCall{}, false
			}
			req.head = true
			req.method = "HEAD"
		case "-O", "--remote-name":
			if req.responsePath != "" || req.discardBody {
				return ReplacementCall{}, false
			}
			req.remoteName = true
		default:
			if i+1 >= len(args) {
				return ReplacementCall{}, false
			}
			i++
			if !req.readValueFlag(flag, args[i]) {
				return ReplacementCall{}, false
			}
		}
	}
	return req.call()
}

// readValueFlag applies a flag that consumes the next argument.
func (r *curlRequest) readValueFlag(flag, value string) bool {
	switch flag {
	case "-X", "--request":
		if r.head || !nativeHTTPMethod(value) {
			return false
		}
		r.method, r.methodExplicit = value, true
	case "-H", "--header":
		return r.addHeader(value)
	case "-A", "--user-agent":
		return r.addHeader("User-Agent: " + value)
	case "-e", "--referer":
		return r.addHeader("Referer: " + value)
	case "-d", "--data", "--data-raw":
		if flag != "--data-raw" && strings.HasPrefix(value, "@") {
			return false
		}
		if r.bodyPath != "" || r.jsonBody || len(r.formParts) > 0 || (r.bodySet && len(r.dataParts) == 0 && len(r.urlencoded) == 0) {
			return false
		}
		r.dataParts = append(r.dataParts, value)
		r.setBody()
	case "--data-urlencode":
		if r.bodyPath != "" || r.jsonBody || len(r.formParts) > 0 {
			return false
		}
		encoded, ok := curlURLEncodedPart(value)
		if !ok {
			return false
		}
		r.urlencoded = append(r.urlencoded, encoded)
		r.setBody()
	case "-F", "--form":
		if r.bodySet && len(r.formParts) == 0 {
			return false
		}
		part, ok := curlFormPart(value)
		if !ok {
			return false
		}
		r.formParts = append(r.formParts, part)
		r.setBody()
	case "-u", "--user":
		user, password, ok := strings.Cut(value, ":")
		if !ok || user == "" || r.basicAuth {
			return false
		}
		r.basicUser, r.basicPassword, r.basicAuth = user, password, true
	case "-c", "--cookie-jar":
		name := cookieJarNameFor(value)
		if name == "" || (r.cookieJar != "" && r.cookieJar != name) {
			return false
		}
		r.cookieJar = name
	case "-b", "--cookie":
		// A value with '=' is a literal Cookie header; otherwise a jar file.
		if strings.Contains(value, "=") {
			if r.cookieHeader != "" {
				return false
			}
			r.cookieHeader = value
			return true
		}
		name := cookieJarNameFor(value)
		if name == "" || (r.cookieJar != "" && r.cookieJar != name) {
			return false
		}
		r.cookieJar = name
	case "--data-binary", "--json":
		if r.bodySet || len(r.formParts) > 0 {
			return false
		}
		if strings.HasPrefix(value, "@") {
			p := strings.TrimPrefix(value, "@")
			if p == "" || p == "-" {
				return false
			}
			r.bodyPath = p
		} else {
			r.bodyText = value
		}
		r.jsonBody = flag == "--json"
		r.setBody()
	case "-m", "--max-time":
		duration, err := time.ParseDuration(value + "s")
		if err != nil || duration < time.Second || duration > 5*time.Minute || duration%time.Millisecond != 0 {
			return false
		}
		r.timeoutMS = int(duration / time.Millisecond)
	case "-o", "--output":
		if r.responsePath != "" || r.discardBody || r.remoteName {
			return false
		}
		switch value {
		case "/dev/null":
			r.discardBody = true
		case "-", "":
			// Explicit stdout is the inline body the result already carries.
			return false
		default:
			r.responsePath = value
		}
	case "-w", "--write-out":
		return curlWriteOutRepresentable(value)
	case "--unix-socket":
		if r.unixSocket != "" || strings.TrimSpace(value) == "" {
			return false
		}
		r.unixSocket = value
	case "--url":
		if r.target != "" {
			return false
		}
		r.target = value
	default:
		return false
	}
	return true
}

func (r *curlRequest) addHeader(raw string) bool {
	name, value, ok := strings.Cut(raw, ":")
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	if !ok || name == "" || value == "" {
		return false
	}
	r.contentType = r.contentType || strings.EqualFold(name, "Content-Type")
	r.headers = append(r.headers, outboundhttp.Header{Name: name, Value: value})
	return true
}

func (r *curlRequest) setBody() {
	r.bodySet = true
	if !r.methodExplicit && !r.head {
		r.method = "POST"
	}
}

// call assembles the http_request arguments once the whole argv is read.
func (r *curlRequest) call() (ReplacementCall, bool) {
	if r.target == "" {
		return ReplacementCall{}, false
	}
	target, err := outboundhttp.NormalizeURL(r.target)
	if err != nil {
		return ReplacementCall{}, false
	}
	if strings.ContainsAny(target.Path, "{}[]") || strings.ContainsAny(target.RawQuery, "{}[]") {
		return ReplacementCall{}, false
	}
	if err := outboundhttp.ValidateHeaders(r.headers); err != nil {
		return ReplacementCall{}, false
	}
	if r.bodySet && (r.method == "GET" || r.method == "HEAD") {
		return ReplacementCall{}, false
	}
	// safe redirects require body-less requests without caller headers.
	if r.follow && (r.bodySet || len(r.headers) > 0 || (r.method != "GET" && r.method != "HEAD")) {
		return ReplacementCall{}, false
	}
	mapped := map[string]any{"url": r.target, "method": r.method}
	if len(r.dataParts) > 0 || len(r.urlencoded) > 0 {
		r.bodyText = strings.Join(append(append([]string(nil), r.dataParts...), r.urlencoded...), "&")
	}
	switch {
	case len(r.formParts) > 0:
		parts := make([]any, 0, len(r.formParts))
		for _, part := range r.formParts {
			parts = append(parts, part)
		}
		mapped["form"] = parts
	case r.bodyPath != "":
		mapped["body_path"] = r.bodyPath
	case r.bodySet:
		mapped["body_text"] = r.bodyText
	}
	headers := make([]any, 0, len(r.headers)+3)
	for _, header := range r.headers {
		headers = append(headers, map[string]any{"name": header.Name, "value": header.Value})
	}
	if r.cookieHeader != "" {
		headers = append(headers, map[string]any{"name": "Cookie", "value": r.cookieHeader})
	}
	if r.bodySet && !r.contentType && len(r.formParts) == 0 {
		contentType := "application/x-www-form-urlencoded"
		if r.jsonBody {
			contentType = "application/json"
		}
		headers = append(headers, map[string]any{"name": "Content-Type", "value": contentType})
	}
	if r.basicAuth {
		if hasHeaderNamed(r.headers, "Authorization") {
			return ReplacementCall{}, false
		}
		mapped["auth"] = map[string]any{"scheme": "basic", "username": r.basicUser, "password": r.basicPassword}
	}
	if r.cookieJar != "" {
		mapped["cookie_jar"] = r.cookieJar
	}
	if r.jsonBody && !hasHeaderNamed(r.headers, "Accept") {
		headers = append(headers, map[string]any{"name": "Accept", "value": "application/json"})
	}
	if len(headers) > 0 {
		mapped["headers"] = headers
	}
	if r.timeoutMS > 0 {
		mapped["timeout_ms"] = r.timeoutMS
	}
	if r.follow {
		mapped["redirects"] = string(outboundhttp.RedirectSafe)
	}
	if r.discardBody {
		mapped["response_body"] = "discard"
	}
	if r.remoteName {
		// -O requires a non-empty trailing path segment.
		name := path.Base(target.Path)
		if strings.HasSuffix(target.Path, "/") || name == "" || name == "." || name == "/" {
			return ReplacementCall{}, false
		}
		r.responsePath = name
	}
	if r.responsePath != "" {
		mapped["response_path"] = filepath.ToSlash(r.responsePath)
	}
	if r.unixSocket != "" {
		// The socket is the destination; the executor reviews it from this argument.
		mapped["unix_socket"] = filepath.ToSlash(r.unixSocket)
	} else if capability, ok := loopbackCapabilityFor(r.target); ok {
		mapped["capability_request"] = capability
	}
	return ReplacementCall{Tool: "http_request", Args: mapped}, true
}

// curlURLEncodedPart maps one --data-urlencode operand.
func curlURLEncodedPart(operand string) (string, bool) {
	equals := strings.IndexByte(operand, '=')
	at := strings.IndexByte(operand, '@')
	if at >= 0 && (equals < 0 || at < equals) {
		return "", false
	}
	if equals < 0 {
		return url.QueryEscape(operand), true
	}
	name, content := operand[:equals], operand[equals+1:]
	if name == "" {
		return url.QueryEscape(content), true
	}
	if strings.ContainsAny(name, "&") {
		return "", false
	}
	return name + "=" + url.QueryEscape(content), true
}

// curlFormPart maps one -F operand.
func curlFormPart(operand string) (map[string]any, bool) {
	name, value, ok := strings.Cut(operand, "=")
	if !ok || name == "" || strings.ContainsAny(name, ";") {
		return nil, false
	}
	// Extra part attributes (type=, filename=) ride after ';'.
	value, attrs, _ := strings.Cut(value, ";")
	part := map[string]any{"name": name}
	switch {
	case strings.HasPrefix(value, "@"):
		p := strings.TrimPrefix(value, "@")
		if p == "" || p == "-" {
			return nil, false
		}
		part["path"] = filepath.ToSlash(p)
	case strings.HasPrefix(value, "<"):
		return nil, false
	default:
		part["value"] = value
	}
	for _, attr := range strings.Split(attrs, ";") {
		key, v, ok := strings.Cut(strings.TrimSpace(attr), "=")
		if !ok {
			if strings.TrimSpace(attr) == "" {
				continue
			}
			return nil, false
		}
		switch key {
		case "type":
			part["content_type"] = v
		case "filename":
			part["filename"] = v
		default:
			return nil, false
		}
	}
	if _, isFile := part["path"]; !isFile && (part["content_type"] != nil || part["filename"] != nil) {
		return nil, false
	}
	return part, true
}

// cookieJarNameFor derives a jar identifier from a cookie file path.
func cookieJarNameFor(file string) string {
	base := path.Base(filepath.ToSlash(strings.TrimSpace(file)))
	if base == "." || base == "/" || base == "-" || base == "" {
		return ""
	}
	name := strings.TrimSuffix(base, path.Ext(base))
	if name == "" {
		name = base
	}
	return name
}

func hasHeaderNamed(headers []outboundhttp.Header, name string) bool {
	for _, header := range headers {
		if strings.EqualFold(header.Name, name) {
			return true
		}
	}
	return false
}

// expandCurlShortFlags splits bundled boolean flags into single-letter forms.
func expandCurlShortFlags(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if len(arg) <= 2 || !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") {
			out = append(out, arg)
			continue
		}
		letters := arg[1:]
		if strings.Trim(letters, curlBooleanShortFlags) != "" {
			return nil, false
		}
		for _, letter := range letters {
			out = append(out, "-"+string(letter))
		}
	}
	return out, true
}

// curlWriteOutRepresentable checks if a write-out format only extracts status code.
func curlWriteOutRepresentable(format string) bool {
	for _, match := range curlWriteOutSubstitution.FindAllStringSubmatch(format, -1) {
		switch match[1] {
		case "http_code", "response_code":
		default:
			return false
		}
	}
	return true
}

func nativeHTTPMethod(method string) bool {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return true
	default:
		return false
	}
}

// httpSequenceReplacement translates chained requests into separate tool calls.
func httpSequenceReplacement(elements []argv.SequenceElement) ([]ReplacementCall, bool) {
	calls := make([]ReplacementCall, 0, len(elements))
	for i, el := range elements {
		if i > 0 && el.Connector != argv.ConnectorSeq && el.Connector != argv.ConnectorAnd {
			return nil, false
		}
		program := filepath.Base(strings.TrimSpace(el.Name))
		if program == "echo" {
			continue
		}
		if commandGrammarFor[program] != "curl" {
			return nil, false
		}
		call, ok := curlReplacement(el.Args)
		if !ok || !replacementPathsRepresentable(call) {
			return nil, false
		}
		calls = append(calls, call)
	}
	if len(calls) == 0 {
		return nil, false
	}
	return calls, true
}
