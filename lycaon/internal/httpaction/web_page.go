package httpaction

import (
	"mime"
	"net/url"
	"strconv"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const fetchURLTool = "fetch_url"

// webPage is a successful remote GET that delivered an HTML document to the
// model. Loopback and socket targets are services under test, not pages to read.
func webPage(spec requestSpec, resp outboundhttp.Response, placement bodyPlacement) bool {
	if spec.method != "GET" || spec.socket != "" || spec.dialsLoopback() {
		return false
	}
	if resp.Status < 200 || resp.Status > 299 {
		return false
	}
	if placement.inline == "" && placement.spillPath == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(resp.ContentType)
	return err == nil && (mediaType == "text/html" || mediaType == "application/xhtml+xml")
}

// reportWebPage states a delivered web page when fetch_url could read it this turn.
func reportWebPage(tctx tools.ToolContext, spec requestSpec, resp outboundhttp.Response, placement bodyPlacement, finalURL string) {
	if tctx.Out == nil || !tctx.TurnToolPlan.Addressable(fetchURLTool) || !webPage(spec, resp, placement) {
		return
	}
	host := spec.target.Hostname()
	if final, err := url.Parse(finalURL); err == nil && final.Hostname() != "" {
		host = final.Hostname()
	}
	tctx.Out.Facts = tctx.Out.Facts.WithFeedback(tools.HTTPRequestWebPageCode, map[string]any{
		"host":               host,
		"bytes":              strconv.FormatInt(resp.Bytes, 10),
		"content_type":       resp.ContentType,
		"fetch_url_deferred": tctx.TurnToolPlan.Deferred(fetchURLTool),
	}, &api.FeedbackSubject{Kind: "host", ID: host})
}
