// Package httpactiontest serves http_request through httpaction.Perform in a
// test registry, so executor-level tests reach the exchange core curl uses.
package httpactiontest

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/tools"
)

// Register adds an http_request handler backed by httpaction.Perform that
// reports the listener as curl does. Its result reports status, final URL and
// body.
func Register(registry *tools.DefaultRegistry, deps httpaction.Deps) error {
	return registry.Register("http_request", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		ex, err := httpaction.Perform(ctx, deps, args, tctx, httpaction.ExchangeOptions{MaxBodyBytes: 4 << 20})
		var transport *httpaction.TransportFailure
		if errors.As(err, &transport) {
			return "", transport.Reject
		}
		if err != nil {
			return "", err
		}
		httpaction.ReportLoopbackListener(tctx, ex.Listener)
		out, err := json.Marshal(map[string]any{
			"status": ex.Response.Status, "final_url": ex.FinalURL, "body": string(ex.Response.Body),
		})
		return string(out), err
	})
}
