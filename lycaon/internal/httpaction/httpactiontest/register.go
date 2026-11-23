// Package httpactiontest serves http_request through httpaction.Perform in a
// test registry, so executor-level tests reach the exchange core curl uses.
package httpactiontest

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/tools"
)

// Register adds an http_request handler backed by httpaction.Perform that
// reports the listener as curl does. Its result reports status, final URL and
// body.
func Register(registry *tools.DefaultRegistry, deps httpaction.Deps) error {
	return registry.Register("http_request", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		opts := httpaction.ExchangeOptions{MaxBodyBytes: 4 << 20, WireHeaders: true, Output: &httpaction.JarOutput{}}
		if body, ok := args["body_text"].(string); ok {
			opts.Slots.Body = &body
		}
		headers, _ := args["headers"].([]any)
		for _, item := range headers {
			h, _ := item.(map[string]any)
			name, _ := h["name"].(string)
			value, _ := h["value"].(string)
			opts.Slots.Headers = append(opts.Slots.Headers, outboundhttp.Header{Name: name, Value: value})
		}
		ex, err := httpaction.Perform(ctx, deps, args, tctx, opts)
		var transport *httpaction.TransportFailure
		if errors.As(err, &transport) {
			return "", transport.Reject
		}
		if err != nil {
			return "", err
		}
		httpaction.ReportLoopbackListener(tctx, ex.Listener)
		out, err := json.Marshal(map[string]any{
			"status": ex.Response.Status, "final_url": ex.FinalURL, "body": opts.Output.Text(string(ex.Response.Body)),
		})
		return string(out), err
	})
}
