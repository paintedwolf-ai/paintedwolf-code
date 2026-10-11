package researchwarm

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// Warmer schedules attributed background warming.
type Warmer interface {
	WarmDeclaredURLsAsync(urlSource, projectID, projectDir, sessionID string, onDone func(api.IndexWarmingMeta))
	WarmSearchAsync(searchQuery, projectID, projectDir, sessionID, toolCallID string, hitURLs, residualURLs []string, strongHits, maxResults int, directParticipated bool, onDone func(api.IndexWarmingMeta))
	WarmFetchAsync(pageURL, title, projectID, projectDir, sessionID, toolCallID string, onDone func(api.IndexWarmingMeta))
	CancelSession(sessionID string)
}

type Store interface {
	Get(context.Context, string) (*api.Session, error)
}
type Admission interface {
	WithSessionTreeAdmission(context.Context, string, func() error) error
}
type Appender func(context.Context, string, ...api.Message) error

type Service struct {
	store     Store
	admission Admission
	append    Appender
	warmer    Warmer
}

func New(store Store, admission Admission, append Appender) *Service {
	return &Service{store: store, admission: admission, append: append}
}
func (s *Service) CancelSession(sessionID string) {
	if s != nil && s.warmer != nil {
		s.warmer.CancelSession(sessionID)
	}
}

func (m *Service) SetWarmer(w Warmer) {
	m.warmer = w
}

func (m *Service) DeclaredURLs(ctx context.Context, sessionID, projectID, urlSource, projectDir string) {
	if m.warmer == nil || strings.TrimSpace(urlSource) == "" {
		return
	}
	doneCtx := context.WithoutCancel(ctx)
	_ = m.admission.WithSessionTreeAdmission(ctx, sessionID, func() error {
		m.warmer.WarmDeclaredURLsAsync(urlSource, projectID, projectDir, sessionID, func(meta api.IndexWarmingMeta) {
			m.appendIndexWarmingMessage(doneCtx, sessionID, meta)
		})
		return nil
	})
}

// Search expands the index after a successful web search.
func (m *Service) Search(ctx context.Context, sessionID, toolCallID, query, projectDir string, hitURLs, residualURLs []string, strongHits, maxResults int, directParticipated bool) {
	if m.warmer == nil || strings.TrimSpace(query) == "" {
		return
	}
	doneCtx := context.WithoutCancel(ctx)
	_ = m.admission.WithSessionTreeAdmission(ctx, sessionID, func() error {
		m.warmer.WarmSearchAsync(query, m.projectIDForCost(ctx, sessionID), projectDir, sessionID, toolCallID, hitURLs, residualURLs, strongHits, maxResults, directParticipated, func(meta api.IndexWarmingMeta) {
			m.appendIndexWarmingMessage(doneCtx, sessionID, meta)
		})
		return nil
	})
}

// Fetch expands the index around a fetched page.
func (m *Service) Fetch(ctx context.Context, sessionID, toolCallID, pageURL, title, projectDir string) {
	if m.warmer == nil || strings.TrimSpace(pageURL) == "" {
		return
	}
	doneCtx := context.WithoutCancel(ctx)
	_ = m.admission.WithSessionTreeAdmission(ctx, sessionID, func() error {
		m.warmer.WarmFetchAsync(pageURL, title, m.projectIDForCost(ctx, sessionID), projectDir, sessionID, toolCallID, func(meta api.IndexWarmingMeta) {
			m.appendIndexWarmingMessage(doneCtx, sessionID, meta)
		})
		return nil
	})
}

func (m *Service) projectIDForCost(ctx context.Context, sessionID string) string {
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
func (m *Service) appendIndexWarmingMessage(ctx context.Context, sessionID string, meta api.IndexWarmingMeta) {
	msg := api.Message{
		Role:         api.MessageRoleSystem,
		Kind:         api.MessageKindIndexWarming,
		Content:      indexWarmingSummary(meta),
		IndexWarming: &meta,
	}
	_ = m.append(ctx, sessionID, msg)
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
