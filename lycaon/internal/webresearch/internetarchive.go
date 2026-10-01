package webresearch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/webindex"
)

func internetArchiveRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "internetarchive",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "internetarchive")
			if base == "" {
				return "", fmt.Errorf("internetarchive endpoint not configured")
			}
			u, err := url.Parse(base + "/advancedsearch.php")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("q", query)
			q.Add("fl[]", "identifier")
			q.Add("fl[]", "title")
			q.Add("fl[]", "description")
			q.Set("rows", fmt.Sprintf("%d", min(max, 25)))
			q.Set("output", "json")
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseInternetArchiveHits,
	}
}

func parseInternetArchiveHits(body []byte) ([]WebHit, error) {
	var data struct {
		Response struct {
			Docs []struct {
				Identifier  string            `json:"identifier"`
				Title       archiveFlexString `json:"title"`
				Description archiveFlexString `json:"description"`
			} `json:"docs"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Response.Docs))
	for _, it := range data.Response.Docs {
		id := strings.TrimSpace(it.Identifier)
		if id == "" {
			continue
		}
		title := strings.TrimSpace(string(it.Title))
		if title == "" {
			title = id
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      "https://archive.org/details/" + url.PathEscape(id),
			Snippet:  truncateText(webindex.NormalizeWebTextForStorage(string(it.Description), 0), 300),
			Provider: "internetarchive",
		})
	}
	return hits, nil
}

// Metadata fields accept either a string or an array of strings.
type archiveFlexString string

func (f *archiveFlexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '[' {
		var arr []string
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*f = archiveFlexString(strings.Join(arr, " "))
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*f = archiveFlexString(s)
	return nil
}
