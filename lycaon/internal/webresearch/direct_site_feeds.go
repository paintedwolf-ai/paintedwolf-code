package webresearch

import (
	"context"
	"encoding/xml"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// maxDiscoveredFeeds bounds how many advertised feeds one site gets to fetch.
const maxDiscoveredFeeds = 3

// discoverFeedURLs reads the feeds a page advertises via
// <link rel="alternate" type="application/rss+xml|atom+xml">.
func discoverFeedURLs(body, siteBase string) []string {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(out) >= maxDiscoveredFeeds {
			return
		}
		if n.Type == html.ElementNode && n.Data == "link" {
			var rel, typ, href string
			for _, attr := range n.Attr {
				switch attr.Key {
				case "rel":
					rel = strings.ToLower(strings.TrimSpace(attr.Val))
				case "type":
					typ = strings.ToLower(strings.TrimSpace(attr.Val))
				case "href":
					href = strings.TrimSpace(attr.Val)
				}
			}
			if strings.Contains(rel, "alternate") && href != "" &&
				(strings.Contains(typ, "rss+xml") || strings.Contains(typ, "atom+xml")) {
				if abs := resolveMaybeRelativeURL(siteBase+"/", href); abs != "" {
					key := canonicalURL(abs)
					if _, ok := seen[key]; !ok {
						seen[key] = struct{}{}
						out = append(out, abs)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

func fetchFeedLinks(ctx context.Context, feedURL string, maxItems int) []siteIndexLink {
	if maxItems <= 0 {
		maxItems = maxRSSItems
	}
	body, err := fetchTextURL(ctx, feedURL)
	if err != nil {
		return nil
	}
	items := parseFeedItems(body, maxItems)
	out := make([]siteIndexLink, 0, len(items))
	for _, item := range items {
		out = append(out, siteIndexLink{Title: item.Title, URL: item.URL, Date: item.Date})
	}
	return out
}

type feedItem struct {
	Title string
	URL   string
	Date  time.Time
}

func parseFeedItems(body string, maxItems int) []feedItem {
	if maxItems <= 0 {
		maxItems = maxRSSItems
	}
	type feedWire struct {
		Channel struct {
			Items []struct {
				Title   string `xml:"title"`
				Link    string `xml:"link"`
				PubDate string `xml:"pubDate"`
			} `xml:"item"`
		} `xml:"channel"`
		Entries []struct {
			Title string `xml:"title"`
			Link  struct {
				Href string `xml:"href,attr"`
			} `xml:"link"`
			Updated   string `xml:"updated"`
			Published string `xml:"published"`
		} `xml:"entry"`
	}
	var feed feedWire
	if err := xml.Unmarshal([]byte(body), &feed); err != nil {
		return nil
	}
	out := make([]feedItem, 0, maxItems)
	for _, item := range feed.Channel.Items {
		if item.Link == "" {
			continue
		}
		out = append(out, feedItem{
			Title: strings.TrimSpace(item.Title),
			URL:   strings.TrimSpace(item.Link),
			Date:  parseWebDate(item.PubDate),
		})
		if len(out) >= maxItems {
			return out
		}
	}
	for _, entry := range feed.Entries {
		if entry.Link.Href == "" {
			continue
		}
		date := parseWebDate(entry.Published)
		if date.IsZero() {
			date = parseWebDate(entry.Updated)
		}
		out = append(out, feedItem{
			Title: strings.TrimSpace(entry.Title),
			URL:   strings.TrimSpace(entry.Link.Href),
			Date:  date,
		})
		if len(out) >= maxItems {
			return out
		}
	}
	return out
}

// webDateLayouts covers sitemap lastmod (W3C datetime), RSS pubDate (RFC 1123 /
// RFC 822), and Atom timestamps (RFC 3339).
var webDateLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04Z07:00",
	"2006-01-02",
	time.RFC1123Z,
	time.RFC1123,
	time.RFC822Z,
	time.RFC822,
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
}

func parseWebDate(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range webDateLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}
