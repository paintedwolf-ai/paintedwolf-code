package webresearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// packageRegistryShapes maps family_params.shape → RESTSpec builder (shared factory).
var packageRegistryShapes = map[string]func(providerID string) RESTSpec{
	"npm":        npmShapedRESTSpec,
	"jsr":        jsrShapedRESTSpec,
	"nuget":      nugetShapedRESTSpec,
	"rubygems":   rubygemsShapedRESTSpec,
	"packagist":  packagistShapedRESTSpec,
	"pkg_go_dev": pkgGoDevShapedRESTSpec,
	"pub_dev":    pubDevShapedRESTSpec,
	"hex":        hexShapedRESTSpec,
	"hackage":    hackageShapedRESTSpec,
}

func packageRegistryRESTSpec(entry CatalogEntry) (RESTSpec, error) {
	shape := strings.TrimSpace(entry.FamilyParams["shape"])
	build, ok := packageRegistryShapes[shape]
	if !ok {
		return RESTSpec{}, fmt.Errorf("unknown package_registry shape %q", shape)
	}
	return build(entry.ID), nil
}

func npmShapedRESTSpec(providerID string) RESTSpec {
	return RESTSpec{
		ProviderID: providerID,
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, providerID)
			if base == "" {
				return "", fmt.Errorf("%s endpoint not configured", providerID)
			}
			u, err := url.Parse(base + "/-/v1/search")
			if err != nil {
				return "", err
			}
			count := max
			if count > 25 {
				count = 25
			}
			q := u.Query()
			q.Set("text", query)
			q.Set("size", fmt.Sprintf("%d", count))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHitsWithSettings: func(body []byte, _ Settings) ([]WebHit, error) {
			return parseNpmHits(body, providerID)
		},
	}
}

func parseNpmHits(body []byte, providerID string) ([]WebHit, error) {
	var data struct {
		Objects []struct {
			Package struct {
				Name        string `json:"name"`
				Version     string `json:"version"`
				Description string `json:"description"`
				Links       struct {
					Npm string `json:"npm"`
				} `json:"links"`
			} `json:"package"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Objects))
	for _, it := range data.Objects {
		name := strings.TrimSpace(it.Package.Name)
		if name == "" {
			continue
		}
		hitURL := strings.TrimSpace(it.Package.Links.Npm)
		if hitURL == "" {
			hitURL = "https://www.npmjs.com/package/" + url.PathEscape(name)
		}
		snippet := strings.TrimSpace(it.Package.Description)
		if ver := strings.TrimSpace(it.Package.Version); ver != "" {
			if snippet != "" {
				snippet += " · "
			}
			snippet += ver
		}
		hits = append(hits, WebHit{
			Title:    name,
			URL:      hitURL,
			Snippet:  snippet,
			Provider: providerID,
		})
	}
	return hits, nil
}

func jsrShapedRESTSpec(providerID string) RESTSpec {
	return RESTSpec{
		ProviderID: providerID,
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, providerID)
			if base == "" {
				return "", fmt.Errorf("%s endpoint not configured", providerID)
			}
			u, err := url.Parse(base + "/api/packages")
			if err != nil {
				return "", err
			}
			count := max
			if count > 25 {
				count = 25
			}
			q := u.Query()
			q.Set("query", query)
			q.Set("limit", fmt.Sprintf("%d", count))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHitsWithSettings: func(body []byte, s Settings) ([]WebHit, error) {
			return parseJsrHits(body, s, providerID)
		},
	}
}

func parseJsrHits(body []byte, s Settings, providerID string) ([]WebHit, error) {
	var data struct {
		Items []struct {
			Scope       string `json:"scope"`
			Name        string `json:"name"`
			Description string `json:"description"`
			LatestVer   string `json:"latestVersion"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(keylessEndpoint(s, providerID), "/")
	if base == "" {
		base = "https://jsr.io"
	}
	hits := make([]WebHit, 0, len(data.Items))
	for _, it := range data.Items {
		scope := strings.TrimSpace(it.Scope)
		name := strings.TrimSpace(it.Name)
		if scope == "" || name == "" {
			continue
		}
		title := "@" + scope + "/" + name
		snippet := strings.TrimSpace(it.Description)
		if ver := strings.TrimSpace(it.LatestVer); ver != "" {
			if snippet != "" {
				snippet += " · "
			}
			snippet += ver
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      base + "/@" + url.PathEscape(scope) + "/" + url.PathEscape(name),
			Snippet:  snippet,
			Provider: providerID,
		})
	}
	return hits, nil
}

func nugetShapedRESTSpec(providerID string) RESTSpec {
	return RESTSpec{
		ProviderID: providerID,
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, providerID)
			if base == "" {
				return "", fmt.Errorf("%s endpoint not configured", providerID)
			}
			u, err := url.Parse(strings.TrimSuffix(base, "/") + "/query")
			if err != nil {
				return "", err
			}
			count := max
			if count > 25 {
				count = 25
			}
			q := u.Query()
			q.Set("q", query)
			q.Set("take", fmt.Sprintf("%d", count))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHitsWithSettings: func(body []byte, _ Settings) ([]WebHit, error) {
			return parseNugetHits(body, providerID)
		},
	}
}

func parseNugetHits(body []byte, providerID string) ([]WebHit, error) {
	var data struct {
		Data []struct {
			ID          string `json:"id"`
			Version     string `json:"version"`
			Description string `json:"description"`
			ProjectURL  string `json:"projectUrl"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Data))
	for _, it := range data.Data {
		id := strings.TrimSpace(it.ID)
		if id == "" {
			continue
		}
		hitURL := strings.TrimSpace(it.ProjectURL)
		if hitURL == "" {
			path := "/packages/" + url.PathEscape(id)
			if ver := strings.TrimSpace(it.Version); ver != "" {
				path += "/" + url.PathEscape(ver)
			}
			hitURL = "https://www.nuget.org" + path
		}
		snippet := strings.TrimSpace(it.Description)
		if ver := strings.TrimSpace(it.Version); ver != "" {
			if snippet != "" {
				snippet += " · "
			}
			snippet += ver
		}
		hits = append(hits, WebHit{
			Title:    id,
			URL:      hitURL,
			Snippet:  snippet,
			Provider: providerID,
		})
	}
	return hits, nil
}

func rubygemsShapedRESTSpec(providerID string) RESTSpec {
	return RESTSpec{
		ProviderID: providerID,
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, providerID)
			if base == "" {
				return "", fmt.Errorf("%s endpoint not configured", providerID)
			}
			u, err := url.Parse(strings.TrimSuffix(base, "/") + "/api/v1/search.json")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("query", query)
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHitsWithSettings: func(body []byte, s Settings) ([]WebHit, error) {
			return parseRubygemsHits(body, s, providerID)
		},
	}
}

func parseRubygemsHits(body []byte, s Settings, providerID string) ([]WebHit, error) {
	var items []struct {
		Name       string `json:"name"`
		Version    string `json:"version"`
		Info       string `json:"info"`
		ProjectURL string `json:"project_uri"`
		Downloads  int64  `json:"downloads"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(keylessEndpoint(s, providerID), "/")
	hits := make([]WebHit, 0, len(items))
	for _, it := range items {
		name := strings.TrimSpace(it.Name)
		if name == "" {
			continue
		}
		hitURL := strings.TrimSpace(it.ProjectURL)
		if hitURL == "" {
			if base == "" {
				base = "https://rubygems.org"
			}
			hitURL = base + "/gems/" + url.PathEscape(name)
		}
		snippet := strings.TrimSpace(it.Info)
		if ver := strings.TrimSpace(it.Version); ver != "" {
			if snippet != "" {
				snippet += " · "
			}
			snippet += ver
		}
		if it.Downloads > 0 {
			if snippet != "" {
				snippet += " · "
			}
			snippet += fmt.Sprintf("%d downloads", it.Downloads)
		}
		hits = append(hits, WebHit{
			Title:    name,
			URL:      hitURL,
			Snippet:  snippet,
			Provider: providerID,
		})
	}
	return hits, nil
}

func packagistShapedRESTSpec(providerID string) RESTSpec {
	return RESTSpec{
		ProviderID: providerID,
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, providerID)
			if base == "" {
				return "", fmt.Errorf("%s endpoint not configured", providerID)
			}
			u, err := url.Parse(base + "/search.json")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("q", query)
			q.Set("per_page", fmt.Sprintf("%d", min(max, 25)))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHits: func(body []byte) ([]WebHit, error) {
			return parsePackagistHits(body, providerID)
		},
	}
}

func parsePackagistHits(body []byte, providerID string) ([]WebHit, error) {
	var data struct {
		Results []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			URL         string `json:"url"`
			Downloads   int64  `json:"downloads"`
			Favers      int64  `json:"favers"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := make([]WebHit, 0, len(data.Results))
	for _, item := range data.Results {
		name := strings.TrimSpace(item.Name)
		hitURL := strings.TrimSpace(item.URL)
		if name == "" || hitURL == "" {
			continue
		}
		parts := make([]string, 0, 3)
		if description := strings.TrimSpace(item.Description); description != "" {
			parts = append(parts, description)
		}
		if item.Downloads > 0 {
			parts = append(parts, fmt.Sprintf("%d downloads", item.Downloads))
		}
		if item.Favers > 0 {
			parts = append(parts, fmt.Sprintf("%d favorites", item.Favers))
		}
		hits = append(hits, WebHit{
			Title:    name,
			URL:      hitURL,
			Snippet:  strings.Join(parts, " · "),
			Provider: providerID,
		})
	}
	return hits, nil
}

func pkgGoDevShapedRESTSpec(providerID string) RESTSpec {
	return RESTSpec{
		ProviderID: providerID,
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, providerID)
			if base == "" {
				return "", fmt.Errorf("%s endpoint not configured", providerID)
			}
			u, err := url.Parse(strings.TrimSuffix(base, "/") + "/v1beta/search")
			if err != nil {
				return "", err
			}
			count := max
			if count > 25 {
				count = 25
			}
			q := u.Query()
			q.Set("q", query)
			q.Set("limit", fmt.Sprintf("%d", count))
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHitsWithSettings: func(body []byte, s Settings) ([]WebHit, error) {
			return parsePkgGoDevHits(body, s, providerID)
		},
	}
}

func parsePkgGoDevHits(body []byte, s Settings, providerID string) ([]WebHit, error) {
	var data struct {
		Items []struct {
			PackagePath string `json:"packagePath"`
			ModulePath  string `json:"modulePath"`
			Version     string `json:"version"`
			Synopsis    string `json:"synopsis"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(keylessEndpoint(s, providerID), "/")
	if base == "" {
		base = "https://pkg.go.dev"
	}
	hits := make([]WebHit, 0, len(data.Items))
	for _, it := range data.Items {
		path := strings.TrimSpace(it.PackagePath)
		if path == "" {
			continue
		}
		title := path
		if mod := strings.TrimSpace(it.ModulePath); mod != "" && mod != path {
			title = mod + " · " + path
		}
		snippet := strings.TrimSpace(it.Synopsis)
		if ver := strings.TrimSpace(it.Version); ver != "" {
			if snippet != "" {
				snippet += " · "
			}
			snippet += ver
		}
		hits = append(hits, WebHit{
			Title:    title,
			URL:      base + "/" + path,
			Snippet:  snippet,
			Provider: providerID,
		})
	}
	return hits, nil
}

func pubDevShapedRESTSpec(providerID string) RESTSpec {
	return RESTSpec{
		ProviderID: providerID,
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, providerID)
			if base == "" {
				return "", fmt.Errorf("%s endpoint not configured", providerID)
			}
			u, err := url.Parse(base + "/api/search")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("q", query)
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHitsWithSettings: func(body []byte, s Settings) ([]WebHit, error) {
			return parsePubDevHits(body, s, providerID)
		},
	}
}

func parsePubDevHits(body []byte, s Settings, providerID string) ([]WebHit, error) {
	var data struct {
		Packages []struct {
			Package string `json:"package"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(keylessEndpoint(s, providerID), "/")
	if base == "" {
		base = "https://pub.dev"
	}
	hits := make([]WebHit, 0, len(data.Packages))
	for _, it := range data.Packages {
		name := strings.TrimSpace(it.Package)
		if name == "" {
			continue
		}
		if len(hits) >= 25 {
			break
		}
		hits = append(hits, WebHit{
			Title:    name,
			URL:      base + "/packages/" + url.PathEscape(name),
			Provider: providerID,
		})
	}
	return hits, nil
}

func hexShapedRESTSpec(providerID string) RESTSpec {
	return RESTSpec{
		ProviderID: providerID,
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, providerID)
			if base == "" {
				return "", fmt.Errorf("%s endpoint not configured", providerID)
			}
			u, err := url.Parse(base + "/api/packages")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("search", query)
			q.Set("sort", "recent_downloads")
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		ParseHitsWithSettings: func(body []byte, s Settings) ([]WebHit, error) {
			return parseHexHits(body, s, providerID)
		},
	}
}

func parseHexHits(body []byte, s Settings, providerID string) ([]WebHit, error) {
	var items []struct {
		Name string `json:"name"`
		Meta struct {
			Description string `json:"description"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(keylessEndpoint(s, providerID), "/")
	if base == "" {
		base = "https://hex.pm"
	}
	hits := make([]WebHit, 0, len(items))
	for _, it := range items {
		name := strings.TrimSpace(it.Name)
		if name == "" {
			continue
		}
		if len(hits) >= 25 {
			break
		}
		hits = append(hits, WebHit{
			Title:    name,
			URL:      base + "/packages/" + url.PathEscape(name),
			Snippet:  strings.TrimSpace(it.Meta.Description),
			Provider: providerID,
		})
	}
	return hits, nil
}

func hackageShapedRESTSpec(providerID string) RESTSpec {
	return RESTSpec{
		ProviderID: providerID,
		Method:     http.MethodGet,
		BuildURL: func(s Settings, query string, max int) (string, error) {
			base := keylessEndpoint(s, providerID)
			if base == "" {
				return "", fmt.Errorf("%s endpoint not configured", providerID)
			}
			u, err := url.Parse(base + "/packages/search")
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("terms", query)
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		BuildRequest: func(req *http.Request, _ Settings) error {
			req.Header.Set("Accept", "application/json")
			return nil
		},
		ParseHitsWithSettings: func(body []byte, s Settings) ([]WebHit, error) {
			return parseHackageHits(body, s, providerID)
		},
	}
}

func parseHackageHits(body []byte, s Settings, providerID string) ([]WebHit, error) {
	var items []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(keylessEndpoint(s, providerID), "/")
	if base == "" {
		base = "https://hackage.haskell.org"
	}
	hits := make([]WebHit, 0, len(items))
	for _, it := range items {
		name := strings.TrimSpace(it.Name)
		if name == "" {
			continue
		}
		if len(hits) >= 25 {
			break
		}
		hits = append(hits, WebHit{
			Title:    name,
			URL:      base + "/package/" + url.PathEscape(name),
			Provider: providerID,
		})
	}
	return hits, nil
}
