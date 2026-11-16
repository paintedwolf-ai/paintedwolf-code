package httpaction

import (
	"context"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/tools"
)

// maxInlineBody bounds what one response puts in front of the model. Anything
// past it lands whole under host data and the result names the file.
const maxInlineBody = 128 << 10

// bodyPlacement is where a response body ended up and what the result may
// therefore claim about it.
type bodyPlacement struct {
	inline   string
	encoding string
	omitted  bool
	// spillPath is host-data relative and readable with read or jq.
	spillPath string
	// spillFailed marks an overflow that could not be landed, so the result
	// reports the body as unavailable.
	spillFailed bool
}

// placeResponseBody decides how a response body reaches the caller. A landed
// or discarded body was never buffered, so there is nothing to place.
func placeResponseBody(ctx context.Context, resp outboundhttp.Response, tctx tools.ToolContext, landed bool, discarded bool) bodyPlacement {
	switch {
	case landed, discarded, !resp.BodyObserved:
		return bodyPlacement{omitted: true}
	case !textual(resp.ContentType, resp.Body):
		return bodyPlacement{omitted: true, encoding: "binary"}
	case len(resp.Body) <= maxInlineBody:
		return bodyPlacement{inline: string(resp.Body), encoding: "utf-8"}
	}
	rel, ok := landHostData(ctx, tctx, resp.Body)
	if !ok {
		return bodyPlacement{omitted: true, encoding: "utf-8", spillFailed: true}
	}
	return bodyPlacement{omitted: true, encoding: "utf-8", spillPath: rel}
}
