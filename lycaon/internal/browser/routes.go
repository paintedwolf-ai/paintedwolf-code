package browser

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// Route fixture bounds.
const (
	MaxRoutes            = 16
	MaxRouteBodyBytes    = 1 << 20
	MaxRouteBodiesBytes  = 4 << 20
	MaxRouteDelayMS      = 30_000
	MaxRouteHeaders      = 16
	defaultRouteMimeType = "application/json"
)

// routeFailures maps the failure names a route may answer with to browser network errors.
var routeFailures = map[string]proto.NetworkErrorReason{
	"failed":                proto.NetworkErrorReasonFailed,
	"aborted":               proto.NetworkErrorReasonAborted,
	"timed_out":             proto.NetworkErrorReasonTimedOut,
	"connection_refused":    proto.NetworkErrorReasonConnectionRefused,
	"connection_reset":      proto.NetworkErrorReasonConnectionReset,
	"name_not_resolved":     proto.NetworkErrorReasonNameNotResolved,
	"internet_disconnected": proto.NetworkErrorReasonInternetDisconnected,
	"blocked":               proto.NetworkErrorReasonBlockedByClient,
}

// RouteRule answers matching page requests without the network. A rule with no status,
// body, or failure lets the request through after its delay.
type RouteRule struct {
	// URL is a full URL or a path starting with "/"; "*" matches any run of characters.
	URL         string            `json:"url"`
	Method      string            `json:"method,omitempty"`
	Status      int               `json:"status,omitempty"`
	Body        string            `json:"body,omitempty"`
	BodyPath    string            `json:"body_path,omitempty"`
	ContentType string            `json:"content_type,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	DelayMS     int               `json:"delay_ms,omitempty"`
	Fail        string            `json:"fail,omitempty"`
	// Times limits how many requests the rule answers; zero answers every match.
	Times int `json:"times,omitempty"`
	// BodyBytes holds a resolved body_path; the tool layer reads it inside the project.
	BodyBytes []byte `json:"-"`
}

func (r RouteRule) answers() bool {
	return r.Status != 0 || r.Body != "" || len(r.BodyBytes) > 0 || r.Fail != ""
}

// ValidateRoutes checks a route table before it reaches a page.
func ValidateRoutes(rules []RouteRule) error {
	if len(rules) > MaxRoutes {
		return browserengine.Reject("CAPTURE_ROUTE_INVALID", map[string]any{"reason": "too_many_routes", "count": len(rules), "max": MaxRoutes})
	}
	total := 0
	for i, r := range rules {
		invalid := func(reason string, extra ...any) error {
			data := map[string]any{"reason": reason, "route": i, "route_url": r.URL}
			for j := 0; j+1 < len(extra); j += 2 {
				data[fmt.Sprint(extra[j])] = extra[j+1]
			}
			return browserengine.Reject("CAPTURE_ROUTE_INVALID", data)
		}
		if strings.TrimSpace(r.URL) == "" {
			return invalid("missing_url")
		}
		if !strings.HasPrefix(r.URL, "/") && !strings.Contains(r.URL, "://") && r.URL != "*" {
			return invalid("url_not_absolute")
		}
		if r.Status != 0 && (r.Status < 100 || r.Status > 599) {
			return invalid("bad_status", "status", r.Status)
		}
		if r.Fail != "" {
			if _, ok := routeFailures[r.Fail]; !ok {
				return invalid("unknown_failure", "fail", r.Fail)
			}
			if r.Status != 0 || r.Body != "" || r.BodyPath != "" {
				return invalid("fail_with_response")
			}
		}
		if r.Body != "" && r.BodyPath != "" {
			return invalid("body_and_body_path")
		}
		if r.DelayMS < 0 || r.DelayMS > MaxRouteDelayMS {
			return invalid("bad_delay", "delay_ms", r.DelayMS, "max_delay_ms", MaxRouteDelayMS)
		}
		if len(r.Headers) > MaxRouteHeaders {
			return invalid("too_many_headers", "max_headers", MaxRouteHeaders)
		}
		size := len(r.Body) + len(r.BodyBytes)
		if size > MaxRouteBodyBytes {
			return invalid("body_too_large", "bytes", size, "max_bytes", MaxRouteBodyBytes)
		}
		total += size
	}
	if total > MaxRouteBodiesBytes {
		return browserengine.Reject("CAPTURE_ROUTE_INVALID", map[string]any{"reason": "bodies_too_large", "bytes": total, "max_bytes": MaxRouteBodiesBytes})
	}
	return nil
}

type compiledRoute struct {
	rule      RouteRule
	pattern   *regexp.Regexp
	pathOnly  bool
	remaining int
}

// routeTable is a page's active route fixtures. Replacing it resets every rule's count.
type routeTable struct {
	mu     sync.Mutex
	routes []*compiledRoute
}

func newRouteTable(rules []RouteRule) *routeTable {
	t := &routeTable{}
	t.replace(rules)
	return t
}

func (t *routeTable) replace(rules []RouteRule) {
	compiled := make([]*compiledRoute, 0, len(rules))
	for _, r := range rules {
		compiled = append(compiled, &compiledRoute{
			rule: r, pattern: globPattern(r.URL), pathOnly: strings.HasPrefix(r.URL, "/"), remaining: r.Times,
		})
	}
	t.mu.Lock()
	t.routes = compiled
	t.mu.Unlock()
}

func (t *routeTable) len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.routes)
}

func globPattern(glob string) *regexp.Regexp {
	parts := strings.Split(glob, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
}

// match returns the first rule for a request and consumes one of its answers.
func (t *routeTable) match(method string, u *url.URL) (RouteRule, int, bool) {
	if t == nil || u == nil {
		return RouteRule{}, 0, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, r := range t.routes {
		if r.rule.Times > 0 && r.remaining == 0 {
			continue
		}
		if m := strings.TrimSpace(r.rule.Method); m != "" && !strings.EqualFold(m, method) {
			continue
		}
		subject := u.String()
		if r.pathOnly {
			subject = u.RequestURI()
			if !r.pattern.MatchString(subject) {
				subject = u.EscapedPath()
			}
		}
		if !r.pattern.MatchString(subject) {
			continue
		}
		if r.rule.Times > 0 {
			r.remaining--
		}
		return r.rule, i, true
	}
	return RouteRule{}, 0, false
}

// routeAnswer turns a matched rule into the page's fetch answer.
func routeAnswer(rule RouteRule, index int) fetchAnswer {
	answer := fetchAnswer{delay: time.Duration(rule.DelayMS) * time.Millisecond, servedBy: networkServedByRoute, route: &index}
	if !rule.answers() {
		answer.pass = true
		return answer
	}
	if rule.Fail != "" {
		answer.fail = routeFailures[rule.Fail]
		return answer
	}
	answer.status = rule.Status
	if answer.status == 0 {
		answer.status = http.StatusOK
	}
	answer.body = []byte(rule.Body)
	if len(rule.BodyBytes) > 0 {
		answer.body = rule.BodyBytes
	}
	answer.headers = map[string]string{}
	for k, v := range rule.Headers {
		answer.headers[http.CanonicalHeaderKey(k)] = v
	}
	if ct := strings.TrimSpace(rule.ContentType); ct != "" {
		answer.headers["Content-Type"] = ct
	} else if _, ok := answer.headers["Content-Type"]; !ok && len(answer.body) > 0 {
		answer.headers["Content-Type"] = defaultRouteMimeType
	}
	return answer
}

func routeAction(drive *pageDrive, act CaptureAction) (json.RawMessage, error) {
	if err := ValidateRoutes(act.Routes); err != nil {
		return nil, err
	}
	drive.routes.replace(act.Routes)
	return surveyjson.Marshal(map[string]any{"ok": true, "routes_active": len(act.Routes)})
}
