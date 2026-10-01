package httpaction

import (
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
)

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
			out[i] = hop
		}
	}
	return render(finalURL), out
}
