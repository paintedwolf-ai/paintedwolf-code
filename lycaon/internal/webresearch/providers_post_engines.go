package webresearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/webindex"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const serperSearchURL = "https://google.serper.dev/search"

func serperRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID:  "serper",
		Method:      http.MethodPost,
		ContentType: "application/json",
		BuildURL: func(s Settings, query string, max int) (string, error) {
			return serperSearchURL, nil
		},
		BuildBody: func(s Settings, query string, max int) ([]byte, error) {
			count := max
			if count > 100 {
				count = 100
			}
			return surveyjson.Marshal(map[string]any{
				"q":   query,
				"num": count,
			})
		},
		BuildRequest: func(req *http.Request, s Settings) error {
			req.Header.Set("X-API-KEY", s.Keys["serper"])
			return nil
		},
		ParseHits: parseSerperHits,
	}
}

func parseSerperHits(body []byte) ([]WebHit, error) {
	var data struct {
		Organic []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"organic"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Organic))
	for _, it := range data.Organic {
		if it.Link == "" {
			continue
		}
		title := webindex.NormalizeWebTextForStorage(it.Title, 0)
		if title == "" {
			title = it.Link
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      it.Link,
			Snippet:  webindex.NormalizeWebTextForStorage(it.Snippet, 0),
			Provider: "serper",
		})
	}
	return hits, nil
}

const tavilySearchURL = "https://api.tavily.com/search"

func tavilyRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID:  "tavily",
		Method:      http.MethodPost,
		ContentType: "application/json",
		BuildURL: func(s Settings, query string, max int) (string, error) {
			return tavilySearchURL, nil
		},
		BuildBody: func(s Settings, query string, max int) ([]byte, error) {
			count := max
			if count > 20 {
				count = 20
			}
			return surveyjson.Marshal(map[string]any{
				"query":        query,
				"max_results":  count,
				"search_depth": "basic",
			})
		},
		BuildRequest: func(req *http.Request, s Settings) error {
			req.Header.Set("Authorization", "Bearer "+s.Keys["tavily"])
			return nil
		},
		ParseHits: parseTavilyHits,
	}
}

func parseTavilyHits(body []byte) ([]WebHit, error) {
	var data struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Results))
	for _, it := range data.Results {
		if it.URL == "" {
			continue
		}
		title := webindex.NormalizeWebTextForStorage(it.Title, 0)
		if title == "" {
			title = it.URL
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      it.URL,
			Snippet:  webindex.NormalizeWebTextForStorage(it.Content, 0),
			Provider: "tavily",
		})
	}
	return hits, nil
}

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
