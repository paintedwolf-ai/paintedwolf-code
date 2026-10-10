package preview

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/lycaon/lycaon/internal/browser"
)

type cleanupProtocol struct{ stops int }

func (*cleanupProtocol) Event() <-chan *cdp.Event { return nil }
func (p *cleanupProtocol) Call(ctx context.Context, _, method string, _ interface{}) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch method {
	case "Target.getTargetInfo":
		return []byte(`{"targetInfo":{"targetId":"page","type":"page","title":"Preview","url":"https://example.test"}}`), nil
	case "Page.stopScreencast":
		p.stops++
		return []byte(`{}`), nil
	default:
		return nil, fmt.Errorf("unexpected preview protocol call %s", method)
	}
}

func TestPreviewCleanupStopsItsCastAfterTheRequestEnds(t *testing.T) {
	for _, action := range []string{"replace", "detach", "unwatch", "close"} {
		t.Run(action, func(t *testing.T) {
			protocol := &cleanupProtocol{}
			page := rod.New().Client(protocol).PageFromSession("protocol-session")
			controller := NewController(DefaultConfig(), nil)
			configurePreviewProjection(controller)
			opts := AttachOpts{ProjectID: "project", SessionID: "session", PageID: "page", AssistantMessageID: "message", ToolCallID: "call", Held: &browser.HeldPage{Page: page, TargetURL: "https://example.test"}}
			controller.Attach(t.Context(), opts)
			key := streamKey("session", "page")
			stream := controller.pages[key]
			if stream == nil {
				t.Fatal("preview did not attach")
			}
			done := make(chan struct{})
			close(done)
			canceled := 0
			stream.cast = &castHandle{cancel: func() { canceled++ }, done: done}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			switch action {
			case "replace":
				controller.Attach(ctx, opts)
			case "detach":
				controller.Detach(ctx, "session", "page")
			case "unwatch":
				controller.watching[key] = true
				controller.SetWatching(ctx, "session", false, "page")
			case "close":
				controller.Close(ctx)
			}
			if canceled != 1 || protocol.stops != 1 || stream.cast != nil {
				t.Fatalf("cleanup after request cancellation: canceled=%d stops=%d cast=%v", canceled, protocol.stops, stream.cast)
			}
			if action == "detach" || action == "close" {
				if len(controller.pages) != 0 {
					t.Fatalf("disposed preview remains attached: %+v", controller.pages)
				}
			} else if controller.pages[key] == nil {
				t.Fatal("cleanup discarded the retained attachment")
			}
		})
	}
}
