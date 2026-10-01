package webresearch

import (
	"fmt"
	"net/http"
	"net/url"
)

func arxivRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "arxiv",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "arxiv")
			if base == "" {
				return "", fmt.Errorf("arxiv endpoint not configured")
			}
			u, err := url.Parse(base + "/api/query")
			if err != nil {
				return "", err
			}
			count := max
			if count > 50 {
				count = 50
			}
			q := u.Query()
			q.Set("search_query", "all:"+query)
			q.Set("max_results", fmt.Sprintf("%d", count))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: func(body []byte) ([]WebHit, error) {
			return parseAtomHits(body, "arxiv", 300)
		},
	}
}
