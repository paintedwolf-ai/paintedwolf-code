package events

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// failingHub rejects every Publish.
type failingHub struct{ err error }

func (h failingHub) Publish(context.Context, api.EventTopic, PublishKey, any) error { return h.err }

func (h failingHub) Subscribe(context.Context, Subscription) (<-chan api.EventEnvelope, func(), error) {
	return nil, func() {}, nil
}

func (h failingHub) SubscriberCount() int { return 0 }

var _ EventHub = failingHub{}

func captureWarnLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logged
}

// Publisher methods log Hub.Publish rejections.
func TestPublisherLogsHubPublishFailures(t *testing.T) {
	cause := errors.New("subscriber fanout failed")

	t.Run("PublishCost", func(t *testing.T) {
		logged := captureWarnLog(t)
		pub := &Publisher{Hub: failingHub{err: cause}}
		pub.PublishCost(context.Background(), "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.CostEvent{})
		if got := logged.String(); !strings.Contains(got, "PublishCost") || !strings.Contains(got, cause.Error()) {
			t.Fatalf("log = %q, want it to name the method and the cause", got)
		}
	})

	t.Run("PublishMessageAppend", func(t *testing.T) {
		logged := captureWarnLog(t)
		pub := &Publisher{Hub: failingHub{err: cause}}
		pub.PublishMessageAppend(context.Background(), "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.Message{ID: "msg-1"})
		if got := logged.String(); !strings.Contains(got, "PublishMessageAppend") || !strings.Contains(got, cause.Error()) {
			t.Fatalf("log = %q, want it to name the method and the cause", got)
		}
	})

	t.Run("PublishMessagePatch", func(t *testing.T) {
		logged := captureWarnLog(t)
		pub := &Publisher{Hub: failingHub{err: cause}}
		pub.PublishMessagePatch(context.Background(), "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.Message{ID: "msg-1"})
		pub.FlushMessagePatches(context.Background(), "sess-1")
		if got := logged.String(); !strings.Contains(got, "PublishMessagePatch") || !strings.Contains(got, cause.Error()) {
			t.Fatalf("log = %q, want it to name the method and the cause", got)
		}
	})

	t.Run("PublishAttention", func(t *testing.T) {
		logged := captureWarnLog(t)
		pub := &Publisher{Hub: failingHub{err: cause}, Attention: staticAttentionSource{}}
		pub.FlushAttention(context.Background())
		if got := logged.String(); !strings.Contains(got, "PublishAttention") || !strings.Contains(got, cause.Error()) {
			t.Fatalf("log = %q, want it to name the method and the cause", got)
		}
	})
}

type staticAttentionSource struct{}

func (staticAttentionSource) BuildView(context.Context) (api.AttentionView, error) {
	return api.AttentionView{}, nil
}
