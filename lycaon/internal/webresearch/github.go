package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

const (
	githubIssuesSearchPath = "/search/issues"
	githubCodeSearchPath   = "/search/code"
)

func githubRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "github",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "github")
			if base == "" {
				base = "https://api.github.com"
			}
			path := githubIssuesSearchPath
			if strings.TrimSpace(s.Keys["github"]) != "" {
				path = githubCodeSearchPath
			}
			u, err := url.Parse(strings.TrimSuffix(base, "/") + path)
			if err != nil {
				return "", err
			}
			count := max
			if count > 100 {
				count = 100
			}
			q := u.Query()
			q.Set("q", query)
			q.Set("per_page", fmt.Sprintf("%d", count))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		BuildRequest: func(req *http.Request, s Settings) error {
			if key := strings.TrimSpace(s.Keys["github"]); key != "" {
				req.Header.Set("Authorization", "Bearer "+key)
				req.Header.Set("Accept", "application/vnd.github.text-match+json")
			} else {
				req.Header.Set("Accept", "application/vnd.github+json")
			}
			req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
			return nil
		},
		ParseHits: parseGithubHits,
	}
}

func parseGithubHits(body []byte) ([]WebHit, error) {
	var envelope struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Items) == 0 {
		return nil, nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Items[0], &probe); err != nil {
		return nil, err
	}
	if _, isCode := probe["path"]; isCode {
		return parseGithubCodeHits(body)
	}
	return parseGithubIssueHits(body)
}

func parseGithubCodeHits(body []byte) ([]WebHit, error) {
	var data struct {
		Items []struct {
			Path       string `json:"path"`
			HTMLURL    string `json:"html_url"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
			TextMatches []struct {
				Fragment string `json:"fragment"`
			} `json:"text_matches"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Items))
	for _, it := range data.Items {
		if it.HTMLURL == "" {
			continue
		}
		title := it.Path
		if repo := it.Repository.FullName; repo != "" {
			if title != "" {
				title = repo + "/" + title
			} else {
				title = repo
			}
		}
		if title == "" {
			title = it.HTMLURL
		}
		snippet := ""
		if len(it.TextMatches) > 0 {
			snippet = it.TextMatches[0].Fragment
		}
		hits = append(hits, WebHit{
			Title:    webindex.NormalizeWebTextForStorage(title, 0),
			URL:      it.HTMLURL,
			Snippet:  webindex.NormalizeWebTextForStorage(snippet, 0),
			Provider: "github",
		})
	}
	return hits, nil
}

func parseGithubIssueHits(body []byte) ([]WebHit, error) {
	var data struct {
		Items []struct {
			Title    string `json:"title"`
			HTMLURL  string `json:"html_url"`
			Body     string `json:"body"`
			State    string `json:"state"`
			Comments int    `json:"comments"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Items))
	for _, it := range data.Items {
		if it.HTMLURL == "" {
			continue
		}
		title := webindex.NormalizeWebTextForStorage(it.Title, 0)
		if title == "" {
			title = it.HTMLURL
		}
		bodySnippet := truncateText(webindex.NormalizeWebTextForStorage(it.Body, 0), 200)
		snippet := fmt.Sprintf("%s · %s · %d comments", bodySnippet, it.State, it.Comments)
		hits = append(hits, WebHit{
			Title:    title,
			URL:      it.HTMLURL,
			Snippet:  strings.TrimSpace(snippet),
			Provider: "github",
		})
	}
	return hits, nil
}
