package httpaction

import (
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/tools"
)

// CookieNotHeldCode rejects a reference to a cookie the jar does not carry to
// this URL.
const CookieNotHeldCode = "HTTP_REQUEST_COOKIE_NOT_HELD"

// cookieReference lets a request echo a cookie it may not read, as a
// double-submit CSRF token requires. The host places the value, so it reaches
// the wire without entering the tool arguments or the transcript.
var cookieReference = regexp.MustCompile(`\{\{cookie:([^{}:]+)\}\}`)

// hasCookieReference reports whether any field carries a reference.
func hasCookieReference(req outboundRequest) bool {
	if cookieReference.Match(req.body) {
		return true
	}
	for _, header := range req.headers {
		if cookieReference.MatchString(header.Value) {
			return true
		}
	}
	return false
}

// resolveCookieReferences substitutes each reference with the cookie the jar
// would send to target. An unheld name is refused rather than sent literally.
func resolveCookieReferences(req outboundRequest, jar *secretcap.CookieJar, target *url.URL) (outboundRequest, *tools.ToolReject) {
	if !hasCookieReference(req) {
		return req, nil
	}
	if jar == nil {
		return outboundRequest{}, invalid(errCookieReferenceNeedsJar)
	}
	var missing []string
	substitute := func(in string) string {
		return cookieReference.ReplaceAllStringFunc(in, func(match string) string {
			name := strings.TrimSpace(cookieReference.FindStringSubmatch(match)[1])
			value, ok := jar.Store.Lookup(target, name)
			if !ok {
				missing = append(missing, name)
				return match
			}
			return value
		})
	}
	out := outboundRequest{
		url:      req.url,
		headers:  make([]outboundhttp.Header, len(req.headers)),
		body:     []byte(substitute(string(req.body))),
		redacted: req.redacted,
		receipt:  req.receipt,
	}
	for i, header := range req.headers {
		out.headers[i] = outboundhttp.Header{Name: header.Name, Value: substitute(header.Value)}
	}
	if len(missing) > 0 {
		return outboundRequest{}, &tools.ToolReject{
			Code: CookieNotHeldCode,
			Data: map[string]any{
				"jar":     jar.Name,
				"missing": sortedUnique(missing),
				"held":    jar.Store.NamesForURL(target),
			},
		}
	}
	if err := outboundhttp.ValidateHeaders(out.headers); err != nil {
		return outboundRequest{}, invalid(err)
	}
	return out, nil
}

func sortedUnique(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
