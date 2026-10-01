package events

import (
	"context"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/pkg/api"
)

// PublishMessageAppend emits an authoritative transcript append for Den.
func (p *Publisher) PublishMessageAppend(ctx context.Context, projectIDOrDir, sessionID string, msg api.Message) {
	if p == nil || p.Hub == nil || msg.ID == "" {
		return
	}
	key := PublishKeyFor(ctx, p.Lookup, projectIDOrDir, sessionID)
	msg = messageview.TranscriptMessage(msg)
	logPublishFailure(ctx, "PublishMessageAppend", api.EventTopicMessage, p.Hub.Publish(ctx, api.EventTopicMessage, key, api.MessageEvent{
		SessionID: sessionID,
		Op:        api.MessageChangeAppend,
		Seq:       msg.Seq,
		Message:   msg,
	}))
}

// PublishMessagePatch emits a coalesced full-row replacement by message id.
func (p *Publisher) PublishMessagePatch(ctx context.Context, projectIDOrDir, sessionID string, msg api.Message) {
	if p == nil || p.Hub == nil || msg.ID == "" {
		return
	}
	key := PublishKeyFor(ctx, p.Lookup, projectIDOrDir, sessionID)
	coalescer := p.messagePatchCoalescer()
	if coalescer == nil {
		return
	}
	msg = messageview.TranscriptMessage(msg)
	coalescer.schedule(ctx, key, api.MessageEvent{
		SessionID: sessionID,
		Op:        api.MessageChangePatch,
		Seq:       msg.Seq,
		Message:   msg,
	})
}

func (p *Publisher) messagePatchCoalescer() *messagePatchCoalescer {
	if p == nil || p.Hub == nil {
		return nil
	}
	p.messagePatchesMu.Lock()
	defer p.messagePatchesMu.Unlock()
	if p.closed.Load() {
		return nil
	}
	if p.messagePatches == nil {
		p.messagePatches = newMessagePatchCoalescer(p.Hub)
	}
	return p.messagePatches
}

// FlushMessagePatches publishes pending message patches for sessionID now.
func (p *Publisher) FlushMessagePatches(ctx context.Context, sessionID string) {
	if c := p.messagePatchCoalescer(); c != nil {
		c.flushSession(ctx, sessionID)
	}
}
