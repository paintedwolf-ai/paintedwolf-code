package events

import (
	"context"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// AttentionLifecycleTopic is true for topics whose delivery changes the attention view.
func AttentionLifecycleTopic(topic api.EventTopic) bool {
	switch topic {
	case api.EventTopicSession, api.EventTopicProject, api.EventTopicCheckpoint, api.EventTopicWorkflow:
		return true
	default:
		return false
	}
}

// AttentionRebuildWindow coalesces attention rebuilds. Each build walks every
// session and its pending checkpoints.
const AttentionRebuildWindow = 250 * time.Millisecond

// AttentionViewSource builds the cross-project attention snapshot.
type AttentionViewSource interface {
	BuildView(ctx context.Context) (api.AttentionView, error)
}

// PublishAttention coalesces cross-project snapshots for every subscriber.
// Revisions keep overlapping builds ordered at delivery.
func (p *Publisher) PublishAttention(ctx context.Context) {
	if p == nil || p.Hub == nil || p.Attention == nil {
		return
	}
	p.attentionMu.Lock()
	defer p.attentionMu.Unlock()
	if p.closed.Load() || p.attentionTimer != nil {
		return
	}
	// The trigger context may cancel before the window elapses; keep its values.
	build := context.WithoutCancel(ctx)
	p.attentionWG.Add(1)
	p.attentionTimer = time.AfterFunc(AttentionRebuildWindow, func() {
		defer p.attentionWG.Done()
		p.attentionMu.Lock()
		if p.closed.Load() {
			p.attentionMu.Unlock()
			return
		}
		p.attentionTimer = nil
		// Stamped at build start so start order, not finish order, wins.
		p.attentionRevision++
		revision := p.attentionRevision
		p.attentionMu.Unlock()
		p.buildAndPublishAttention(build, revision)
	})
}

func (p *Publisher) buildAndPublishAttention(ctx context.Context, revision uint64) {
	view, err := p.Attention.BuildView(ctx)
	if err != nil {
		slog.WarnContext(ctx, "attention snapshot failed; retaining the last published view", "error", err)
		return
	}
	p.attentionDeliveryMu.Lock()
	defer p.attentionDeliveryMu.Unlock()
	p.attentionMu.Lock()
	// A completed newer build supersedes this result.
	if p.closed.Load() || revision < p.attentionPublished {
		p.attentionMu.Unlock()
		return
	}
	// Keep acceptance and delivery ordered, including a delayed hub publish.
	p.attentionPublished = revision
	p.attentionMu.Unlock()
	logPublishFailure(ctx, "PublishAttention", api.EventTopicAttention, p.Hub.Publish(ctx, api.EventTopicAttention, PublishKey{}, view))
}

// SettleAttention delivers a pending coalesced rebuild now and waits for
// in-flight builds, so a caller observes attention work already started.
// With nothing pending it publishes nothing.
func (p *Publisher) SettleAttention(ctx context.Context) {
	if p == nil || p.Hub == nil || p.Attention == nil {
		return
	}
	p.attentionMu.Lock()
	pending := p.attentionTimer != nil && !p.closed.Load()
	p.attentionMu.Unlock()
	if pending {
		p.FlushAttention(ctx)
	}
	p.attentionWG.Wait()
}

// FlushAttention publishes immediately and cancels a pending rebuild.
func (p *Publisher) FlushAttention(ctx context.Context) {
	if p == nil || p.Hub == nil || p.Attention == nil {
		return
	}
	p.attentionMu.Lock()
	if p.closed.Load() {
		p.attentionMu.Unlock()
		return
	}
	if p.attentionTimer != nil {
		if p.attentionTimer.Stop() {
			p.attentionWG.Done()
		}
		p.attentionTimer = nil
	}
	p.attentionRevision++
	revision := p.attentionRevision
	p.attentionWG.Add(1)
	p.attentionMu.Unlock()
	defer p.attentionWG.Done()
	p.buildAndPublishAttention(ctx, revision)
}
