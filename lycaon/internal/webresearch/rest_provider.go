package webresearch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// providerOutcome is one provider invocation result before fusion.
type providerOutcome struct {
	providerID string
	ok         bool
	reason     string
	detail     string
	hits       []WebHit
	httpStatus int
	// durationMs is stamped by runProviderTasks for the search summary line.
	durationMs       int64
	gateRecordStatus int
}

// RESTSpec describes a GET/POST search provider backed by HTTP.
type RESTSpec struct {
	ProviderID   string
	Method       string
	BuildURL     func(s Settings, query string, max int) (string, error)
	BuildBody    func(s Settings, query string, max int) ([]byte, error)
	ContentType  string
	BuildRequest func(req *http.Request, s Settings) error
	ParseHits    func(body []byte) ([]WebHit, error)
	// ParseHitsWithSettings is used when hit URLs depend on configured endpoint base.
	ParseHitsWithSettings func(body []byte, s Settings) ([]WebHit, error)
	GateRecordStatus      func(body []byte) (int, bool)
	// TimeoutSec overrides Settings.PerProviderTimeoutSec when positive.
	TimeoutSec int
}

// RESTSearchProvider implements SearchProvider from a RESTSpec.
type RESTSearchProvider struct {
	spec                 RESTSpec
	kind                 ProviderKind
	allowPrivateEndpoint bool
}

func NewRESTSearchProvider(spec RESTSpec, kind ProviderKind) *RESTSearchProvider {
	return &RESTSearchProvider{spec: spec, kind: kind}
}

func (p *RESTSearchProvider) ID() string { return p.spec.ProviderID }

func (p *RESTSearchProvider) Kind() ProviderKind { return p.kind }

func (p *RESTSearchProvider) Configured(s Settings) bool {
	return providerConfigured(p.spec.ProviderID, p.kind, s)
}

func (p *RESTSearchProvider) providerTimeoutSec(s Settings) int {
	if p.spec.TimeoutSec > 0 {
		return p.spec.TimeoutSec
	}
	return s.PerProviderTimeoutSec
}

func (p *RESTSearchProvider) Search(ctx context.Context, s Settings, query string, maxResults int) providerOutcome {
	providerID := p.spec.ProviderID
	if !p.Configured(s) {
		return providerOutcome{
			providerID: providerID,
			reason:     "auth_missing",
			detail:     "provider not configured",
		}
	}
	// The context’s fan-out destination covers all providers.
	dest := screenDestination{
		id:    secretmatch.DestinationKey(providerID, keylessEndpoint(s, providerID)),
		label: providerID,
	}
	screenedQuery, err := screenOutbound(ctx, secretmatch.SurfaceWebSearch, dest, query)
	if err != nil {
		return providerOutcome{providerID: providerID, reason: "secret_denied", detail: err.Error()}
	}
	query = screenedQuery
	url, err := p.spec.BuildURL(s, query, maxResults)
	if err != nil {
		return providerOutcome{providerID: providerID, reason: "parse_error", detail: err.Error()}
	}
	var bodyReader io.Reader
	if p.spec.BuildBody != nil {
		body, err := p.spec.BuildBody(s, query, maxResults)
		if err != nil {
			return providerOutcome{providerID: providerID, reason: "parse_error", detail: err.Error()}
		}
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, p.spec.Method, url, bodyReader)
	if err != nil {
		return providerOutcome{providerID: providerID, reason: "parse_error", detail: err.Error()}
	}
	if ct := strings.TrimSpace(p.spec.ContentType); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", providerSearchUserAgent)
	}
	if p.spec.BuildRequest != nil {
		if err := p.spec.BuildRequest(req, s); err != nil {
			return providerOutcome{providerID: providerID, reason: "parse_error", detail: err.Error()}
		}
	}
	body, status, err := doProviderHTTP(ctx, req, p.providerTimeoutSec(s), p.allowPrivateEndpoint)
	if err != nil {
		reason := "parse_error"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			reason = "timeout"
		} else if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			reason = "cut"
		}
		return providerOutcome{providerID: providerID, reason: reason, detail: err.Error()}
	}
	if status < 200 || status >= 300 {
		return providerOutcome{
			providerID: providerID,
			reason:     "http_error",
			detail:     fmt.Sprintf("HTTP %d", status),
			httpStatus: status,
		}
	}
	var hits []WebHit
	if p.spec.ParseHitsWithSettings != nil {
		hits, err = p.spec.ParseHitsWithSettings(body, s)
	} else if p.spec.ParseHits != nil {
		hits, err = p.spec.ParseHits(body)
	} else {
		return providerOutcome{providerID: providerID, reason: "parse_error", detail: "missing ParseHits"}
	}
	if err != nil {
		return providerOutcome{providerID: providerID, reason: "parse_error", detail: err.Error()}
	}
	parsedHitCount := len(hits)
	out := providerOutcome{providerID: providerID, ok: true, reason: "ok", hits: hits, httpStatus: status}
	if p.spec.GateRecordStatus != nil {
		if gateStatus, ok := p.spec.GateRecordStatus(body); ok {
			out.gateRecordStatus = gateStatus
		}
	}
	for i := range out.hits {
		if out.hits[i].Provider == "" {
			out.hits[i].Provider = providerID
		}
	}
	out.hits = validProviderHits(out.hits)
	if parsedHitCount > 0 && len(out.hits) == 0 {
		out.ok = false
		out.reason = "parse_error"
		out.detail = "provider returned no valid HTTP result URLs"
	}
	return out
}

func validProviderHits(hits []WebHit) []WebHit {
	valid := hits[:0]
	for _, hit := range hits {
		rawURL := strings.TrimSpace(hit.URL)
		u, err := url.Parse(rawURL)
		if err != nil || u.Hostname() == "" || u.User != nil {
			continue
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
		default:
			continue
		}
		hit.URL = rawURL
		valid = append(valid, hit)
	}
	return valid
}
