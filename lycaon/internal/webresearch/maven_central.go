package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func mavenCentralRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "maven_central",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "maven_central")
			if base == "" {
				return "", fmt.Errorf("maven_central endpoint not configured")
			}
			u, err := url.Parse(base + "/solrsearch/select")
			if err != nil {
				return "", err
			}
			count := min(max, 25)
			q := u.Query()
			q.Set("q", query)
			q.Set("rows", fmt.Sprintf("%d", count))
			q.Set("wt", "json")
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseMavenCentralHits,
	}
}

func parseMavenCentralHits(body []byte) ([]WebHit, error) {
	var data struct {
		Response struct {
			Docs []struct {
				GroupID       string `json:"g"`
				ArtifactID    string `json:"a"`
				LatestVersion string `json:"latestVersion"`
				Packaging     string `json:"p"`
			} `json:"docs"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Response.Docs))
	for _, doc := range data.Response.Docs {
		groupID := strings.TrimSpace(doc.GroupID)
		artifactID := strings.TrimSpace(doc.ArtifactID)
		if groupID == "" || artifactID == "" {
			continue
		}
		title := groupID + ":" + artifactID
		parts := make([]string, 0, 2)
		if version := strings.TrimSpace(doc.LatestVersion); version != "" {
			parts = append(parts, version)
		}
		if packaging := strings.TrimSpace(doc.Packaging); packaging != "" {
			parts = append(parts, packaging)
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      "https://central.sonatype.com/artifact/" + url.PathEscape(groupID) + "/" + url.PathEscape(artifactID),
			Snippet:  strings.Join(parts, " · "),
			Provider: "maven_central",
		})
	}
	return hits, nil
}
