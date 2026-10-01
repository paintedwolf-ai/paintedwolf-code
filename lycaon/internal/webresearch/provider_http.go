package webresearch

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/lycaon/lycaon/internal/egressgate"
)

const providerSearchUserAgent = "painted-wolf-code-search/1.0"

// providerHTTPClient overrides the egress-pinned client in tests.
var providerHTTPClient *http.Client

// SetProviderHTTPClientForTest sets the REST client for boundary tests.
func SetProviderHTTPClientForTest(c *http.Client) {
	if os.Getenv("LYCAON_TEST") != "1" {
		panic("webresearch.SetProviderHTTPClientForTest requires LYCAON_TEST=1")
	}
	providerHTTPClient = c
}

func providerHTTPTimeout(timeoutSec int) time.Duration {
	timeout := time.Duration(timeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	return timeout
}

func doProviderHTTP(ctx context.Context, req *http.Request, timeoutSec int, allowPrivate bool) ([]byte, int, error) {
	// Apply the host gate before starting the network deadline.
	if err := egressgate.AwaitHost(ctx, req.URL.Hostname()); err != nil {
		return nil, 0, err
	}
	timeout := providerHTTPTimeout(timeoutSec)
	ctx, cancel := egressgate.IOContext(ctx, timeout)
	defer cancel()
	req = req.WithContext(ctx)

	client, err := providerEgressClient(ctx, req.URL, allowPrivate)
	if err != nil {
		return nil, 0, err
	}
	if providerHTTPClient != nil {
		// Validation precedes the injected transport.
		client = providerHTTPClient
	}
	return readProviderHTTP(client, req)
}

func readProviderHTTP(client *http.Client, req *http.Request) ([]byte, int, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, sanitizeProviderHTTPError(req, err)
	}
	body, status, err := readProviderHTTPBody(resp)
	_ = resp.Body.Close()
	if !resp.Uncompressed || !errors.Is(err, gzip.ErrHeader) ||
		req.Method != http.MethodGet || (req.Body != nil && req.Body != http.NoBody) ||
		req.Context().Err() != nil {
		return body, status, err
	}

	// Retry malformed automatic gzip decoding once under the same deadline.
	retry := req.Clone(req.Context())
	retry.Header.Set("Accept-Encoding", "identity")
	resp, err = client.Do(retry)
	if err != nil {
		return nil, 0, sanitizeProviderHTTPError(retry, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return readProviderHTTPBody(resp)
}

func readProviderHTTPBody(resp *http.Response) ([]byte, int, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body, resp.StatusCode, err
}
