package webresearch

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	grobotstxt "github.com/jimsmart/grobotstxt"
	"github.com/lycaon/lycaon/internal/observability"
)

func fetchTextURL(ctx context.Context, rawURL string) (string, error) {
	body, status, err := fetchIndexBody(ctx, rawURL)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", io.ErrUnexpectedEOF
	}
	return body, nil
}

func fetchIndexBody(ctx context.Context, rawURL string) (body string, status int, err error) {
	u, err := normalizeFetchURL(rawURL)
	if err != nil {
		return "", 0, err
	}
	stats := statsFrom(ctx)
	if stats != nil && !stats.acquireCrawlFetch() {
		stats.addCrawlFetchCapped()
		return "", 0, context.Canceled
	}
	fetchStart := time.Now()
	status = 0
	defer func() {
		elapsed := time.Since(fetchStart)
		if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			stats.addCrawlFetchCanceled(elapsed)
		} else {
			stats.addCrawlFetch(elapsed)
		}
		observability.LogWebSearchFetch(observability.WebSearchFetchCapture{
			SearchID: stats.id(),
			Phase:    stats.currentPhase(),
			Kind:     "index",
			URL:      rawURL,
			Host:     strings.ToLower(u.Host),
			Status:   status,
			FetchMs:  elapsed.Milliseconds(),
		})
	}()
	resp, _, err := politeGuardedGet(ctx, u, acceptHeaderForMode("text"), false)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	status = resp.StatusCode
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return "", resp.StatusCode, readErr
	}
	return string(raw), resp.StatusCode, nil
}

func fetchRobotsTxt(ctx context.Context, siteBase string) string {
	body, status, err := fetchIndexBody(ctx, siteBase+"/robots.txt")
	if err != nil || status < 200 || status >= 300 {
		return ""
	}
	return body
}

func robotsSitemapURLs(robotsBody, siteBase string) []string {
	if robotsBody == "" {
		return nil
	}
	locs := grobotstxt.Sitemaps(robotsBody)
	out := make([]string, 0, len(locs))
	for _, loc := range locs {
		if abs := resolveMaybeRelativeURL(siteBase, loc); abs != "" {
			out = append(out, abs)
		}
	}
	return out
}

func urlAllowedByRobots(robotsBody, pageURL string) bool {
	if robotsBody == "" {
		return true
	}
	return grobotstxt.AgentAllowed(robotsBody, directSearchUserAgent, pageURL)
}
