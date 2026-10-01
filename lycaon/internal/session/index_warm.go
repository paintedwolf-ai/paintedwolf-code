package session

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// IndexWarmer schedules attributed background warming.
type IndexWarmer interface {
	WarmDeclaredURLsAsync(urlSource, projectID, projectDir, sessionID string, onDone func(api.IndexWarmingMeta))
	WarmSearchAsync(searchQuery, projectID, projectDir, sessionID, toolCallID string, hitURLs, residualURLs []string, strongHits, maxResults int, directParticipated bool, onDone func(api.IndexWarmingMeta))
	WarmFetchAsync(pageURL, title, projectID, projectDir, sessionID, toolCallID string, onDone func(api.IndexWarmingMeta))
	CancelSession(sessionID string)
}

// SetIndexWarmer wires background web-index warming into the session manager.
func (m *Manager) SetIndexWarmer(w IndexWarmer) {
	m.indexWarmer = w
}

func (m *Manager) warmIndexForDeclaredURLs(ctx context.Context, sessionID, projectID, urlSource, projectDir string) {
	if m.indexWarmer == nil || strings.TrimSpace(urlSource) == "" {
		return
	}
	doneCtx := context.WithoutCancel(ctx)
	_ = m.WithSessionTreeAdmission(ctx, sessionID, func() error {
		m.indexWarmer.WarmDeclaredURLsAsync(urlSource, projectID, projectDir, sessionID, func(meta api.IndexWarmingMeta) {
			m.appendIndexWarmingMessage(doneCtx, sessionID, meta)
		})
		return nil
	})
}

// WarmIndexForSearch expands the index after a successful web search.
func (m *Manager) WarmIndexForSearch(ctx context.Context, sessionID, toolCallID, query, projectDir string, hitURLs, residualURLs []string, strongHits, maxResults int, directParticipated bool) {
	if m.indexWarmer == nil || strings.TrimSpace(query) == "" {
		return
	}
	doneCtx := context.WithoutCancel(ctx)
	_ = m.WithSessionTreeAdmission(ctx, sessionID, func() error {
		m.indexWarmer.WarmSearchAsync(query, m.projectIDForCost(ctx, sessionID), projectDir, sessionID, toolCallID, hitURLs, residualURLs, strongHits, maxResults, directParticipated, func(meta api.IndexWarmingMeta) {
			m.appendIndexWarmingMessage(doneCtx, sessionID, meta)
		})
		return nil
	})
}

// WarmIndexForFetch expands the index around a fetched page.
func (m *Manager) WarmIndexForFetch(ctx context.Context, sessionID, toolCallID, pageURL, title, projectDir string) {
	if m.indexWarmer == nil || strings.TrimSpace(pageURL) == "" {
		return
	}
	doneCtx := context.WithoutCancel(ctx)
	_ = m.WithSessionTreeAdmission(ctx, sessionID, func() error {
		m.indexWarmer.WarmFetchAsync(pageURL, title, m.projectIDForCost(ctx, sessionID), projectDir, sessionID, toolCallID, func(meta api.IndexWarmingMeta) {
			m.appendIndexWarmingMessage(doneCtx, sessionID, meta)
		})
		return nil
	})
}

func (m *Manager) projectIDForCost(ctx context.Context, sessionID string) string {
	if m == nil || m.store == nil || strings.TrimSpace(sessionID) == "" {
		return ""
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return ""
	}
	return strings.TrimSpace(sess.ProjectID)
}

// appendIndexWarmingMessage records the warming as a transcript row with its chicklet metadata.
func (m *Manager) appendIndexWarmingMessage(ctx context.Context, sessionID string, meta api.IndexWarmingMeta) {
	msg := api.Message{
		Role:         api.MessageRoleSystem,
		Kind:         api.MessageKindIndexWarming,
		Content:      indexWarmingSummary(meta),
		IndexWarming: &meta,
	}
	_ = m.appendMessages(ctx, sessionID, msg)
}

// indexWarmingSummary returns the row's plain-text content.
func indexWarmingSummary(meta api.IndexWarmingMeta) string {
	var b strings.Builder
	b.WriteString("Warmed web index")
	if meta.Pages > 0 || len(meta.Hosts) > 0 {
		fmt.Fprintf(&b, " — %d host", len(meta.Hosts))
		if len(meta.Hosts) != 1 {
			b.WriteString("s")
		}
		fmt.Fprintf(&b, ", %d page", meta.Pages)
		if meta.Pages != 1 {
			b.WriteString("s")
		}
	}
	if meta.Topic != "" {
		fmt.Fprintf(&b, " · %s", meta.Topic)
	}
	if meta.SkipReason != "" && meta.Pages == 0 && len(meta.Hosts) == 0 {
		fmt.Fprintf(&b, " (%s)", meta.SkipReason)
	}
	return b.String()
}
