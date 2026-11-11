package httpaction

import (
	"context"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
)

// ExchangeProvenance summarizes external HTTP provenance for a completed command exchange.
type ExchangeProvenance = evidence.ExchangeProvenance

// WithExchangeProvenanceObserver attaches a callback that receives ExchangeProvenance
// for each HTTP exchange completed within ctx.
func WithExchangeProvenanceObserver(ctx context.Context, fn func(ExchangeProvenance)) context.Context {
	return evidence.WithExchangeProvenanceObserver(ctx, fn)
}

// ReportExchangeProvenance notifies the context's exchange provenance observer, if any.
func ReportExchangeProvenance(ctx context.Context, p ExchangeProvenance) {
	evidence.ReportExchangeProvenance(ctx, p)
}

func hopIsLocal(target *url.URL, resolve []outboundhttp.ResolveMapping) bool {
	if target == nil || !egress.SyntacticLoopback(target.Hostname()) {
		return false
	}
	_, _, ok := hopLoopbackTarget(target, resolve)
	return ok
}

func externalObservedURLs(secrets *secretcap.Resolution, spec requestSpec, finalURL string, redirects []outboundhttp.Hop) []string {
	if spec.socket != "" || spec.unixSocket != "" {
		return nil
	}
	render := func(s string) string { return s }
	if secrets != nil {
		render = secrets.ReferenceValues
	}
	var candidates []*url.URL
	if spec.target != nil {
		candidates = append(candidates, spec.target)
	}
	for _, hop := range redirects {
		if u, err := outboundhttp.NormalizeURL(hop.URL); err == nil {
			candidates = append(candidates, u)
		}
	}
	if u, err := outboundhttp.NormalizeURL(finalURL); err == nil {
		candidates = append(candidates, u)
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(raw string) {
		u := strings.TrimSpace(render(raw))
		if u == "" {
			return
		}
		if _, ok := seen[u]; ok {
			return
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	for _, u := range candidates {
		if u == nil || hopIsLocal(u, spec.resolve) {
			continue
		}
		add(u.String())
		if routes, err := selectedHopRouteURLs(u, spec.resolve); err == nil {
			for _, route := range routes {
				if route != nil && !egress.SyntacticLoopback(route.Hostname()) {
					add(route.String())
				}
			}
		}
	}
	return out
}

// observedURLs reports the redirect chain and final URL as the exchange saw
// them, except that each secret value the call substituted reads as its
// reference token. The request's own URL then matches its canonical
// arguments, and a server-chosen URL keeps every other byte.
func observedURLs(secrets *secretcap.Resolution, finalURL string, hops []outboundhttp.Hop) (string, []outboundhttp.Hop) {
	render := secrets.ReferenceValues
	var out []outboundhttp.Hop
	if len(hops) > 0 {
		out = make([]outboundhttp.Hop, len(hops))
		for i, hop := range hops {
			hop.URL, hop.Location = render(hop.URL), render(hop.Location)
			hop.Headers = renderHeaders(render, hop.Headers)
			if hop.Wire != nil {
				wire := *hop.Wire
				wire.Headers = renderHeaders(render, wire.Headers)
				hop.Wire = &wire
			}
			out[i] = hop
		}
	}
	return render(finalURL), out
}

// renderHeaders presents redirect header values with resolved secrets shown
// as their references, as hop URLs are.
func renderHeaders(render func(string) string, headers []outboundhttp.Header) []outboundhttp.Header {
	out := make([]outboundhttp.Header, len(headers))
	for i, h := range headers {
		out[i] = outboundhttp.Header{Name: h.Name, Value: render(h.Value)}
	}
	return out
}
