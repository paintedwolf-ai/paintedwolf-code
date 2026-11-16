package httpaction

import (
	"net/url"
	"strconv"

	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const fetchURLTool = "fetch_url"

// reportWebPage states a delivered web page when fetch_url could read it this turn.
func reportWebPage(tctx tools.ToolContext, spec requestSpec, resp outboundhttp.Response, placement bodyPlacement, finalURL string) {
	if tctx.Effects.Out == nil || !tctx.Turn.TurnToolPlan.Addressable(fetchURLTool) || !webPage(spec, resp, placement) {
		return
	}
	host := spec.target.Hostname()
	if final, err := url.Parse(finalURL); err == nil && final.Hostname() != "" {
		host = final.Hostname()
	}
	tctx.Effects.Out.Facts = tctx.Effects.Out.Facts.WithFeedback(toolrejection.HTTPRequestWebPageCode, map[string]any{
		"host":         host,
		"bytes":        strconv.FormatInt(resp.Bytes, 10),
		"content_type": resp.ContentType,
	}, &api.FeedbackSubject{Kind: "host", ID: host})
}
