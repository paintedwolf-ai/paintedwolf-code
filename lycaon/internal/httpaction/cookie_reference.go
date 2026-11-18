package httpaction

import (
	"errors"
	"regexp"
	"sort"
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

// errReferenceInURL refuses a cookie or token reference where the host never
// places one: it would reach the recipient, and the request's history, as
// literal text.
var errReferenceInURL = errors.New("{{cookie:…}} and {{token:…}} references are placed in headers and the body, not the URL or host_header")

func referencesOutsideTheirFields(rawURL string, hostHeader any) error {
	host, _ := hostHeader.(string)
	for _, field := range []string{rawURL, host} {
		if cookieReference.MatchString(field) || tokenReference.MatchString(field) {
			return errReferenceInURL
		}
	}
	return nil
}
