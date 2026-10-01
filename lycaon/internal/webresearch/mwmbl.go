package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

const mwmblDefaultSearchPath = "/api/v1/search/"

func mwmblRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "mwmbl",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			// Default endpoint comes from the catalog via resolveProviderConfig.
			base := strings.TrimSuffix(strings.TrimSpace(s.Config["mwmbl"]["endpoint"]), "/")
			if base == "" {
				return "", fmt.Errorf("mwmbl endpoint not configured")
			}
			u, err := url.Parse(base + mwmblDefaultSearchPath)
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("s", query)
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseMwmblHits,
	}
}

type mwmblTextPart struct {
	Value string `json:"value"`
}

type mwmblResultRow struct {
	URL     string          `json:"url"`
	Title   []mwmblTextPart `json:"title"`
	Extract []mwmblTextPart `json:"extract"`
}

func mwmblJoinText(parts []mwmblTextPart) string {
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Value)
	}
	return strings.TrimSpace(b.String())
}

func parseMwmblHits(body []byte) ([]WebHit, error) {
	var rows []mwmblResultRow
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("parse mwmbl response: %w", err)
	}
	hits := make([]WebHit, 0, len(rows))
	for _, it := range rows {
		if it.URL == "" {
			continue
		}
		snippet := mwmblJoinText(it.Extract)
		title := webindex.NormalizeWebTextForStorage(mwmblJoinText(it.Title), 0)
		if title == "" {
			title = it.URL
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      it.URL,
			Snippet:  webindex.NormalizeWebTextForStorage(snippet, 0),
			Provider: "mwmbl",
		})
	}
	return hits, nil
}
