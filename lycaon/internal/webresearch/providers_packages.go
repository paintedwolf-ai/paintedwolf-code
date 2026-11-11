package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func cranRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "cran",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "cran")
			if base == "" {
				return "", fmt.Errorf("cran endpoint not configured")
			}
			u, err := url.Parse(base + "/package/_search")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("q", query)
			q.Set("size", fmt.Sprintf("%d", min(max, 25)))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseCranHits,
	}
}

func parseCranHits(body []byte) ([]WebHit, error) {
	var data struct {
		Hits struct {
			Hits []struct {
				ID     string `json:"_id"`
				Source struct {
					Title   string `json:"Title"`
					Version string `json:"Version"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Hits.Hits))
	for _, it := range data.Hits.Hits {
		name := strings.TrimSpace(it.ID)
		if name == "" {
			continue
		}
		snippet := strings.Join(strings.Fields(strings.TrimSpace(it.Source.Title)), " ")
		if ver := strings.TrimSpace(it.Source.Version); ver != "" {
			if snippet != "" {
				snippet += " · "
			}
			snippet += ver
		}
		hits = append(hits, WebHit{
			Title:    name,
			URL:      "https://cran.r-project.org/package=" + url.PathEscape(name),
			Snippet:  snippet,
			Provider: "cran",
		})
	}
	return hits, nil
}

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

func metacpanRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "metacpan",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "metacpan")
			if base == "" {
				return "", fmt.Errorf("metacpan endpoint not configured")
			}
			u, err := url.Parse(base + "/v1/release/_search")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("q", query)
			q.Set("size", fmt.Sprintf("%d", min(max, 25)*2))
			q.Set("_source", "distribution,abstract,version,author")
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseMetacpanHits,
	}
}

func parseMetacpanHits(body []byte) ([]WebHit, error) {
	var data struct {
		Hits struct {
			Hits []struct {
				Source struct {
					Distribution string `json:"distribution"`
					Abstract     string `json:"abstract"`
					Version      string `json:"version"`
					Author       string `json:"author"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	// Release search returns one hit per release; keep the highest-scored hit
	// per distribution so the same module does not flood the results.
	seen := make(map[string]struct{}, len(data.Hits.Hits))
	hits := make([]WebHit, 0, len(data.Hits.Hits))
	for _, it := range data.Hits.Hits {
		dist := strings.TrimSpace(it.Source.Distribution)
		if dist == "" {
			continue
		}
		if _, dup := seen[dist]; dup {
			continue
		}
		seen[dist] = struct{}{}
		if len(hits) >= 25 {
			break
		}
		snippet := strings.TrimSpace(it.Source.Abstract)
		if ver := strings.TrimSpace(it.Source.Version); ver != "" {
			if snippet != "" {
				snippet += " · "
			}
			snippet += ver
		}
		if author := strings.TrimSpace(it.Source.Author); author != "" {
			if snippet != "" {
				snippet += " · "
			}
			snippet += author
		}
		hits = append(hits, WebHit{
			Title:    dist,
			URL:      "https://metacpan.org/dist/" + url.PathEscape(dist),
			Snippet:  snippet,
			Provider: "metacpan",
		})
	}
	return hits, nil
}

func caniuseRESTSpec() RESTSpec {
	return RESTSpec{
		ProviderID: "caniuse",
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, "caniuse")
			if base == "" {
				return "", fmt.Errorf("caniuse endpoint not configured")
			}
			u, err := url.Parse(base + "/process/query.php")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("search", query)
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: parseCanIUseHits,
	}
}

func parseCanIUseHits(body []byte) ([]WebHit, error) {
	var data struct {
		FeatureIDs []string `json:"featureIds"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	count := min(len(data.FeatureIDs), 20)
	hits := make([]WebHit, 0, count)
	for _, rawID := range data.FeatureIDs[:count] {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		hits = append(hits, WebHit{
			Title:    strings.ReplaceAll(id, "-", " "),
			URL:      "https://caniuse.com/" + url.PathEscape(id),
			Snippet:  "Browser compatibility reference",
			Provider: "caniuse",
		})
	}
	return hits, nil
}
