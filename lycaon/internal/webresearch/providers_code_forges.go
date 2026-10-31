package webresearch

import (
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/webindex"
	"net/http"
	"net/url"
	"strings"
	wire "github.com/lycaon/lycaon/pkg/api"
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

func huggingfaceRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "huggingface",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "huggingface")
			if base == "" {
				return "", fmt.Errorf("huggingface endpoint not configured")
			}
			u, err := url.Parse(base + "/api/models")
			if err != nil {
				return "", err
			}
			count := max
			if count > 50 {
				count = 50
			}
			q := u.Query()
			q.Set("search", query)
			q.Set("limit", fmt.Sprintf("%d", count))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHitsWithSettings: parseHuggingFaceHits,
	}
}

func parseHuggingFaceHits(body []byte, s Settings) ([]WebHit, error) {
	var items []struct {
		ID          string `json:"id"`
		ModelID     string `json:"modelId"`
		PipelineTag string `json:"pipeline_tag"`
		Downloads   int    `json:"downloads"`
		Likes       int    `json:"likes"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(keylessEndpoint(s, "huggingface"), "/")
	hits := make([]WebHit, 0, len(items))
	for _, it := range items {
		modelID := strings.TrimSpace(it.ModelID)
		if modelID == "" {
			modelID = strings.TrimSpace(it.ID)
		}
		if modelID == "" {
			continue
		}
		title := modelID
		hitURL := huggingFaceModelURL(base, modelID)
		snippetParts := make([]string, 0, 3)
		if tag := strings.TrimSpace(it.PipelineTag); tag != "" {
			snippetParts = append(snippetParts, tag)
		}
		if it.Downloads > 0 {
			snippetParts = append(snippetParts, fmt.Sprintf("%d downloads", it.Downloads))
		}
		if it.Likes > 0 {
			snippetParts = append(snippetParts, fmt.Sprintf("%d likes", it.Likes))
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      hitURL,
			Snippet:  strings.Join(snippetParts, " · "),
			Provider: "huggingface",
		})
	}
	return hits, nil
}

func huggingFaceModelURL(base, modelID string) string {
	modelID = strings.TrimPrefix(strings.TrimSpace(modelID), "/")
	if base == "" {
		return "https://huggingface.co/" + modelID
	}
	return base + "/" + modelID
}

const (
	directCardKind  = "direct"
	directCardLabel = "Direct search"
)

// DirectCardContent builds the direct-search card payload for settings UI.
func DirectCardContent() wire.WebResearchDirectCardContent {
	return wire.WebResearchDirectCardContent{
		ProviderID: string(wire.WebSearchProviderDirect),
		Kind:       directCardKind,
		Label:      directCardLabel,
	}
}
