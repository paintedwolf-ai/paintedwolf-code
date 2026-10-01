package webresearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const ietfRFCProviderID = "ietf_rfc"

func newIETFRFCProvider() *ietfRFCProvider {
	return &ietfRFCProvider{}
}

type ietfRFCProvider struct{}

func (p *ietfRFCProvider) ID() string { return ietfRFCProviderID }

func (p *ietfRFCProvider) Kind() ProviderKind { return KindKeyless }

func (p *ietfRFCProvider) Configured(s Settings) bool {
	return providerConfigured(ietfRFCProviderID, KindKeyless, s)
}

func (p *ietfRFCProvider) Search(ctx context.Context, s Settings, query string, maxResults int) providerOutcome {
	if !p.Configured(s) {
		return providerOutcome{
			providerID: ietfRFCProviderID,
			reason:     "auth_missing",
			detail:     "provider not configured",
		}
	}
	count := maxResults
	if count <= 0 {
		count = 10
	}
	if count > 25 {
		count = 25
	}
	q := shortenProviderQuery(query)
	filters := []string{"title__icontains", "abstract__icontains"}
	if looksLikeRFCName(query) || looksLikeRFCName(q) {
		// Document name is rfcNNNN — only useful when the query is that shape.
		filters = []string{"name__icontains", "title__icontains"}
		if looksLikeRFCName(query) {
			q = strings.Map(func(r rune) rune {
				if r == ' ' || r == '\t' {
					return -1
				}
				return r
			}, strings.ToLower(strings.TrimSpace(query)))
		}
	}

	type fetchResult struct {
		body   []byte
		status int
		err    error
	}
	results := make(chan fetchResult, len(filters))
	for _, filter := range filters {
		rawURL, err := ietfRFCSearchURL(s, filter, q, count)
		if err != nil {
			return providerOutcome{providerID: ietfRFCProviderID, reason: "parse_error", detail: err.Error()}
		}
		go func(rawURL string) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
			if err != nil {
				results <- fetchResult{err: err}
				return
			}
			req.Header.Set("User-Agent", providerSearchUserAgent)
			req.Header.Set("Accept", "application/json")
			body, status, err := doProviderHTTP(ctx, req, s.PerProviderTimeoutSec, false)
			results <- fetchResult{body: body, status: status, err: err}
		}(rawURL)
	}

	var hits []WebHit
	seen := make(map[string]struct{}, count*2)
	var lastStatus int
	var lastErr error
	okCount := 0
	for i := 0; i < len(filters); i++ {
		fr := <-results
		if fr.err != nil {
			lastErr = fr.err
			continue
		}
		lastStatus = fr.status
		if fr.status < 200 || fr.status >= 300 {
			lastErr = fmt.Errorf("HTTP %d", fr.status)
			continue
		}
		part, err := parseIETFRFCHits(fr.body)
		if err != nil {
			lastErr = err
			continue
		}
		okCount++
		for _, h := range part {
			if _, dup := seen[h.URL]; dup {
				continue
			}
			seen[h.URL] = struct{}{}
			hits = append(hits, h)
			if len(hits) >= count {
				break
			}
		}
	}
	if len(hits) == 0 && okCount == 0 {
		reason := "parse_error"
		detail := "ietf_rfc search failed"
		if lastErr != nil {
			detail = lastErr.Error()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				reason = "timeout"
			} else if errors.Is(ctx.Err(), context.Canceled) {
				reason = "cut"
			} else if strings.HasPrefix(detail, "HTTP ") {
				reason = "http_error"
			}
		}
		return providerOutcome{
			providerID: ietfRFCProviderID,
			reason:     reason,
			detail:     detail,
			httpStatus: lastStatus,
		}
	}
	return providerOutcome{
		providerID: ietfRFCProviderID,
		ok:         true,
		reason:     "ok",
		hits:       hits,
		httpStatus: lastStatus,
	}
}

func ietfRFCSearchURL(s Settings, filter, query string, max int) (string, error) {
	base := keylessEndpoint(s, ietfRFCProviderID)
	if base == "" {
		return "", fmt.Errorf("ietf_rfc endpoint not configured")
	}
	u, err := url.Parse(strings.TrimSuffix(base, "/") + "/api/v1/doc/document/")
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("type", "rfc")
	q.Set(filter, query)
	q.Set("limit", fmt.Sprintf("%d", max))
	q.Set("format", "json")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func parseIETFRFCHits(body []byte) ([]WebHit, error) {
	var data struct {
		Objects []struct {
			Name      string `json:"name"`
			Title     string `json:"title"`
			Abstract  string `json:"abstract"`
			RFC       string `json:"rfc"`
			RFCNumber int    `json:"rfc_number"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Objects))
	seen := make(map[string]struct{}, len(data.Objects))
	for _, it := range data.Objects {
		hitURL := ietfRFCPageURL(it.Name, it.RFC, it.RFCNumber)
		if hitURL == "" {
			continue
		}
		if _, ok := seen[hitURL]; ok {
			continue
		}
		seen[hitURL] = struct{}{}
		title := strings.TrimSpace(it.Title)
		if num := ietfRFCNumber(it.RFC, it.RFCNumber); num != "" {
			if title != "" {
				title = "RFC " + num + ": " + title
			} else {
				title = "RFC " + num
			}
		}
		if title == "" {
			title = strings.TrimSpace(it.Name)
		}
		if title == "" {
			title = hitURL
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      hitURL,
			Snippet:  truncateText(strings.TrimSpace(it.Abstract), 280),
			Provider: ietfRFCProviderID,
		})
	}
	return hits, nil
}

func ietfRFCNumber(rfc string, rfcNumber int) string {
	if rfcNumber > 0 {
		return fmt.Sprintf("%d", rfcNumber)
	}
	rfc = strings.TrimSpace(rfc)
	if rfc != "" {
		return rfc
	}
	return ""
}

func ietfRFCPageURL(name, rfc string, rfcNumber int) string {
	num := ietfRFCNumber(rfc, rfcNumber)
	if num == "" {
		name = strings.ToLower(strings.TrimSpace(name))
		if strings.HasPrefix(name, "rfc") {
			num = strings.TrimPrefix(name, "rfc")
		}
	}
	num = strings.TrimSpace(num)
	if num == "" {
		return ""
	}
	return "https://www.rfc-editor.org/rfc/rfc" + url.PathEscape(num) + ".html"
}
