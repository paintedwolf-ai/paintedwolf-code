package toolusage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func (c *liveClient) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	ref, err := url.Parse(path)
	if err != nil || ref.IsAbs() || ref.Host != "" || !strings.HasPrefix(ref.Path, "/") || ref.Fragment != "" {
		return nil, fmt.Errorf("sidecar request requires an absolute path without another origin or fragment")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body) // #nosec G704 -- The operator selects the sidecar origin; callers supply paths, never destinations.
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}
