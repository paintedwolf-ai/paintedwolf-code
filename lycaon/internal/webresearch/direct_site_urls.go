package webresearch

import (
	"net/url"
	"strings"
)

func seedHostsSet(seeds []string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, seed := range seeds {
		if h := urlHost(normalizePageURL(seed)); h != "" {
			out[strings.ToLower(h)] = struct{}{}
		}
		if h := urlHost(normalizeSiteBase(seed)); h != "" {
			out[strings.ToLower(h)] = struct{}{}
		}
	}
	return out
}

func pageSeedURL(raw string) string {
	normalized := normalizeSeed(raw)
	if normalized == "" {
		return ""
	}
	u, err := url.Parse(normalized)
	if err != nil {
		return ""
	}
	if strings.TrimSuffix(u.Path, "/") == "" {
		return ""
	}
	return normalized
}

func normalizePageURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

func urlHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

func normalizeSiteBase(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	path := strings.ToLower(u.Path)
	if strings.HasSuffix(path, "/llms.txt") || path == "/llms.txt" {
		u.Path = ""
	}
	u.Fragment = ""
	u.RawQuery = ""
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path != "" && u.Path != "/" {
		// Deep paths are usually pages, not site roots — use host only for index discovery.
		u.Path = ""
	}
	return strings.TrimRight(u.String(), "/")
}

func resolveMaybeRelativeURL(base, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return b.ResolveReference(r).String()
}

func sitemapTitle(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return u.Host
	}
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		path = path[idx+1:]
	}
	return strings.ReplaceAll(path, "-", " ")
}
