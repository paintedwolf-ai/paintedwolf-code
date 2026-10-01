package kick

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

type kickEngineConfig struct {
	engine prompts.PromptTemplateEngine
}

// QueuePendingGuidance queues guidance by structured identity.
func (k *KickEngine) QueuePendingGuidance(sessionID, text string, feedback api.ToolFeedback) {
	if k == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(text) == "" || strings.TrimSpace(feedback.Code) == "" {
		return
	}
	k.sessionKickQueue(sessionID).push(kickQueueItem{
		kickID: guidanceKickID(feedback), nudge: strings.TrimSpace(text),
	})
}

func guidanceKickID(feedback api.ToolFeedback) string {
	code := strings.TrimSpace(feedback.Code)
	if feedback.Subject == nil || strings.TrimSpace(feedback.Subject.Kind) == "" || strings.TrimSpace(feedback.Subject.ID) == "" {
		return "guidance:" + code
	}
	return "guidance:" + code + ":" + strings.TrimSpace(feedback.Subject.Kind) + ":" + strings.TrimSpace(feedback.Subject.ID)
}

// KickEngine queues coordinator and worker nudges.
type KickEngine struct {
	cfg          atomic.Pointer[kickEngineConfig]
	kickQueues   sync.Map // sessionID -> *sessionKickQueue
	policyQueues sync.Map // sessionID -> *policyQueue
	nextLease    atomic.Uint64
}

// SetPromptEngine installs kick rendering.
func (k *KickEngine) SetPromptEngine(engine prompts.PromptTemplateEngine) {
	if k == nil {
		return
	}
	k.cfg.Store(&kickEngineConfig{engine: engine})
}

// config returns the current rendering configuration.
func (k *KickEngine) config() kickEngineConfig {
	if k == nil {
		return kickEngineConfig{}
	}
	if c := k.cfg.Load(); c != nil {
		return *c
	}
	return kickEngineConfig{}
}

// QueuePendingText queues rendered guidance for the next prompt.
func (k *KickEngine) QueuePendingText(sessionID, text, kickID string) {
	if k == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(text) == "" || strings.TrimSpace(kickID) == "" {
		return
	}
	k.sessionKickQueue(sessionID).push(kickQueueItem{kickID: strings.TrimSpace(kickID), nudge: strings.TrimSpace(text)})
}

// QueueDeferred queues a template for prompt-time rendering.
func (k *KickEngine) QueueDeferred(sessionID, templateID string, opts ...KickOption) {
	if k == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(templateID) == "" {
		return
	}
	var meta kickMeta
	for _, opt := range opts {
		if opt != nil {
			opt(&meta)
		}
	}
	k.sessionKickQueue(sessionID).push(kickQueueItem{
		kickID:   strings.TrimSpace(templateID),
		subject:  meta.subject,
		deferred: true,
		meta:     cloneKickMeta(meta),
		batchSeq: meta.batchSeq,
		hasSeq:   meta.batchSeqSet,
	})
}

// QueueDeferredLatest replaces older guidance in the same lifecycle.
func (k *KickEngine) QueueDeferredLatest(sessionID, latestKey, templateID string, opts ...KickOption) {
	if k == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(latestKey) == "" || strings.TrimSpace(templateID) == "" {
		return
	}
	var meta kickMeta
	for _, opt := range opts {
		if opt != nil {
			opt(&meta)
		}
	}
	k.sessionKickQueue(sessionID).pushLatest(kickQueueItem{
		kickID: strings.TrimSpace(templateID), subject: meta.subject, latestKey: strings.TrimSpace(latestKey),
		deferred: true, meta: cloneKickMeta(meta), batchSeq: meta.batchSeq, hasSeq: meta.batchSeqSet,
	}, &k.nextLease)
}

// DropPendingKicksForBatchSeq removes one batch's kicks.
func (k *KickEngine) DropPendingKicksForBatchSeq(sessionID string, batchSeq int) {
	if k == nil || strings.TrimSpace(sessionID) == "" || batchSeq <= 0 {
		return
	}
	k.sessionKickQueue(sessionID).dropForBatchSeq(batchSeq)
}

// DropPendingKicksBeforeBatchSeq removes stale batch kicks.
func (k *KickEngine) DropPendingKicksBeforeBatchSeq(sessionID string, liveSeq int) {
	if k == nil || strings.TrimSpace(sessionID) == "" || liveSeq <= 0 {
		return
	}
	k.sessionKickQueue(sessionID).dropBeforeBatchSeq(liveSeq)
}

// DropKickID removes matching queued and staged kicks.
func (k *KickEngine) DropKickID(sessionID, kickID string) {
	if k == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	kickID = strings.TrimSpace(kickID)
	if sessionID == "" || kickID == "" {
		return
	}
	k.sessionKickQueue(sessionID).dropKickID(kickID)
}

// ClearPending drops queued and staged kicks.
func (k *KickEngine) ClearPending(sessionID string) {
	if k == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	k.sessionKickQueue(sessionID).clear()
	k.policyQueue(sessionID).clear()
}

// ForgetSession removes the session queue entry.
func (k *KickEngine) ForgetSession(sessionID string) {
	if k == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	k.kickQueues.Delete(sessionID)
	k.policyQueues.Delete(sessionID)
}

// HasQueuedKick reports whether guidance with this id is staged or queued and
// has not yet been rendered into a turn.
func (k *KickEngine) HasQueuedKick(sessionID, kickID string) bool {
	if k == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(kickID) == "" {
		return false
	}
	return k.sessionKickQueue(sessionID).has(strings.TrimSpace(kickID))
}

// PeekPendingKickID returns the next kick identifier.
func (k *KickEngine) PeekPendingKickID(sessionID string) (string, bool) {
	if k == nil {
		return "", false
	}
	item, ok := k.sessionKickQueue(sessionID).peek()
	return item.kickID, ok && item.kickID != ""
}

// TakePendingKickID stages the next queued kick.
func (k *KickEngine) TakePendingKickID(sessionID string) string {
	return k.TakePendingKickIDUnless(sessionID, nil)
}

// TakePendingKickIDUnless stages the next kick skip does not drop.
func (k *KickEngine) TakePendingKickIDUnless(sessionID string, skip KickSkip) string {
	if k == nil {
		return ""
	}
	return k.sessionKickQueue(sessionID).stage(skip, &k.nextLease)
}

// PendingKickIDsUnless drops the kicks skip reports and lists the rest in
// the order a turn delivers them.
func (k *KickEngine) PendingKickIDsUnless(sessionID string, skip KickSkip) []string {
	if k == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	return k.sessionKickQueue(sessionID).prune(skip)
}

// RenderPendingNudge renders a staged nudge and returns its lease.
func (k *KickEngine) RenderPendingNudge(ctx context.Context, sessionID string, live CoordinatorKickRenderContext) (string, uint64, bool, error) {
	if k == nil {
		return "", 0, false, nil
	}
	queue := k.sessionKickQueue(sessionID)
	item := queue.stagedItem()
	if item == nil {
		return "", 0, false, nil
	}
	if item.deferred {
		if item.hasSeq && live.BatchSeq > 0 && item.batchSeq > 0 && item.batchSeq < live.BatchSeq {
			queue.dropStaged(item)
			return "", 0, false, nil
		}
		var text string
		var err error
		if item.eager {
			text, err = k.renderEagerKick(ctx, item.kickID, item.eagerData)
		} else {
			text, err = k.renderCoordinatorKick(ctx, item.kickID, item.meta, live)
		}
		if err != nil {
			return "", 0, false, err
		}
		if strings.TrimSpace(text) == "" {
			return "", 0, false, fmt.Errorf("kick %q rendered empty", item.kickID)
		}
		return strings.TrimSpace(text), item.lease, true, nil
	}
	return item.nudge, item.lease, item.nudge != "", nil
}

// HasLatest reports whether a kick is held under latestKey.
func (k *KickEngine) HasLatest(sessionID, latestKey string) bool {
	if k == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(latestKey) == "" {
		return false
	}
	_, ok := k.sessionKickQueue(sessionID).latest(strings.TrimSpace(latestKey))
	return ok
}

// RenderLatest renders the kick held under latestKey wherever it waits,
// without disturbing the kick staged for the next turn start. A kick from an
// earlier coordinator batch is dropped unrendered. AckLatest consumes it.
func (k *KickEngine) RenderLatest(ctx context.Context, sessionID, latestKey string, live CoordinatorKickRenderContext) (kickID, text string, lease uint64, ok bool, err error) {
	if k == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(latestKey) == "" {
		return "", "", 0, false, nil
	}
	latestKey = strings.TrimSpace(latestKey)
	queue := k.sessionKickQueue(sessionID)
	item, found := queue.latest(latestKey)
	if !found {
		return "", "", 0, false, nil
	}
	if item.hasSeq && live.BatchSeq > 0 && item.batchSeq > 0 && item.batchSeq < live.BatchSeq {
		queue.dropLatest(latestKey, item.lease)
		return "", "", 0, false, nil
	}
	text, err = k.renderCoordinatorKick(ctx, item.kickID, item.meta, live)
	if err != nil {
		return "", "", 0, false, err
	}
	if strings.TrimSpace(text) == "" {
		return "", "", 0, false, fmt.Errorf("kick %q rendered empty", item.kickID)
	}
	return item.kickID, strings.TrimSpace(text), item.lease, true, nil
}

// AckLatest consumes a kick RenderLatest rendered; a newer replacement stays.
func (k *KickEngine) AckLatest(sessionID, latestKey string, lease uint64) {
	if k == nil || strings.TrimSpace(sessionID) == "" || lease == 0 {
		return
	}
	k.sessionKickQueue(sessionID).dropLatest(strings.TrimSpace(latestKey), lease)
}

// AckPendingNudge consumes the matching rendered lease.
func (k *KickEngine) AckPendingNudge(sessionID string, lease uint64) {
	if k == nil || strings.TrimSpace(sessionID) == "" || lease == 0 {
		return
	}
	k.sessionKickQueue(sessionID).ack(lease)
}

// QueueEager stages worker-spawn data for rendering at the take boundary.
func (k *KickEngine) QueueEager(sessionID, templateID string, data map[string]string) {
	if k == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	templateID = strings.TrimSpace(templateID)
	if templateID == "" {
		return
	}
	captured := make(map[string]string, len(data))
	for key, v := range data {
		captured[key] = v
	}
	item := kickQueueItem{
		kickID: templateID, deferred: true, eager: true, eagerData: captured,
	}
	k.sessionKickQueue(sessionID).stageEager(item, &k.nextLease)
}

func (k *KickEngine) renderEagerKick(ctx context.Context, templateID string, data map[string]string) (string, error) {
	cfg := k.config()
	if cfg.engine == nil {
		return "", fmt.Errorf("kick %q: prompt engine not configured", templateID)
	}
	vars := make(map[string]any, len(data))
	for key, value := range data {
		vars[key] = value
	}
	text, err := cfg.engine.RenderKick(ctx, templateID, vars)
	if err != nil {
		return "", fmt.Errorf("render kick %q: %w", templateID, err)
	}
	return text, nil
}

func FormatKickRelativeAgo(now, t time.Time) string {
	d := now.Sub(t)
	if d < 0 {
		d = -d
	}
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
