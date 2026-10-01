package filebriefing

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/runeclamp"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Service) completeFileBriefing(ctx context.Context, briefing Briefing, prompt, projectDir string) {
	startedAt := time.Now()
	if s.generator == nil {
		if briefing.Trigger == "automatic" {
			s.previewFileBriefing(ctx, briefing, "", false)
		} else {
			s.failFileBriefing(ctx, briefing, "AI explanation is unavailable")
		}
		return
	}
	generationCtx, cancel := context.WithTimeout(curationctx.WithoutLane(ctx), s.config.GenerationTimeout())
	defer cancel()
	emitter := newFileBriefingDeltaEmitter(ctx, s, briefing)
	var collected strings.Builder
	collectedRunes := 0
	streamClamped := false
	generatedText, err := s.generator.Generate(
		generationCtx, GenerationRequest{ProjectID: briefing.ProjectID, ProjectDir: projectDir, Trigger: briefing.Trigger, SystemPrompt: s.config.SystemPrompt, Prompt: prompt, MaxTokens: s.config.Generation.MaxOutputTokens},
		func(delta string) {
			remaining := s.config.Generation.MaxOutputChars - collectedRunes
			boundedDelta := runeclamp.Fit(delta, max(remaining, 0))
			streamClamped = streamClamped || boundedDelta != delta
			collected.WriteString(boundedDelta)
			collectedRunes += utf8.RuneCountInString(boundedDelta)
			emitter.Add(generationCtx, boundedDelta)
		},
	)
	if ctx.Err() != nil {
		return
	}
	emitter.Flush(ctx)
	if strings.TrimSpace(generatedText) == "" {
		generatedText = collected.String()
	}
	generatedText = strings.TrimSpace(generatedText)
	boundedText := runeclamp.Fit(generatedText, s.config.Generation.MaxOutputChars)
	truncated := streamClamped || boundedText != generatedText
	generatedText = boundedText
	if err != nil {
		if partial := completedPartial(generatedText, s.config.Generation.MinPartialChars); partial != "" {
			generatedText, truncated = partial, true
		} else if briefing.Trigger == "automatic" {
			s.previewFileBriefing(ctx, briefing, "", false)
			return
		} else {
			s.failFileBriefing(ctx, briefing, "AI detail did not finish within the interactive budget")
			slog.InfoContext(ctx, "file briefing failed", "project_id", briefing.ProjectID, "path", briefing.Path,
				"presentation", briefing.Presentation, "trigger", briefing.Trigger, "target_key", briefing.TargetKey,
				"duration_ms", time.Since(startedAt).Milliseconds(), "error", err)
			return
		}
	}
	if generatedText == "" && briefing.Trigger == "automatic" {
		s.previewFileBriefing(ctx, briefing, "", false)
		return
	}
	if generatedText == "" {
		s.failFileBriefing(ctx, briefing, "AI explanation returned no detail")
		return
	}
	sections := ParseSections(generatedText)
	if len(sections) == 0 {
		s.previewFileBriefing(ctx, briefing, generatedText, truncated)
		return
	}
	s.gate.RLock()
	defer s.gate.RUnlock()
	if ctx.Err() != nil || s.admissionError() != nil {
		return
	}
	outcome := Outcome{
		ProjectID: briefing.ProjectID, RootID: briefing.RootID, Path: briefing.Path, TargetKey: briefing.TargetKey,
		AttemptID: briefing.AttemptID, Sections: sections, Truncated: truncated, UpdatedAt: time.Now().UTC(),
	}
	if err := s.store.Complete(ctx, outcome); err != nil {
		if !errors.Is(err, ErrNotFound) {
			s.logger.WarnContext(ctx, "complete file briefing", "error", err)
		}
		return
	}
	briefing.Status, briefing.Truncated, briefing.Error, briefing.UpdatedAt =
		StatusComplete, truncated, "", outcome.UpdatedAt
	briefing.Sections = outcome.Sections
	s.publishFileBriefingEvent(context.WithoutCancel(ctx), fileBriefingEvent(briefing, "complete"))
	s.maintainFileBriefings(context.WithoutCancel(ctx), briefing)
	slog.InfoContext(ctx, "file briefing complete", "project_id", briefing.ProjectID, "path", briefing.Path,
		"presentation", briefing.Presentation, "trigger", briefing.Trigger, "target_key", briefing.TargetKey,
		"duration_ms", time.Since(startedAt).Milliseconds(), "generated_chars", utf8.RuneCountInString(generatedText), "truncated", truncated)
}

func (s *Service) previewFileBriefing(
	ctx context.Context,
	briefing Briefing,
	fallbackText string,
	truncated bool,
) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if ctx.Err() != nil || s.admissionError() != nil {
		return
	}
	now := time.Now().UTC()
	if err := s.store.Preview(ctx, Outcome{
		ProjectID: briefing.ProjectID, RootID: briefing.RootID, Path: briefing.Path,
		TargetKey: briefing.TargetKey, AttemptID: briefing.AttemptID,
		FallbackText: fallbackText, Truncated: truncated, UpdatedAt: now,
	}); err != nil {
		if !errors.Is(err, ErrNotFound) {
			s.logger.WarnContext(ctx, "keep file briefing preview", "error", err)
		}
		return
	}
	briefing.Status, briefing.Error, briefing.FallbackText, briefing.Truncated, briefing.UpdatedAt =
		StatusPreview, "", fallbackText, truncated, now
	s.publishFileBriefingEvent(context.WithoutCancel(ctx), fileBriefingEvent(briefing, "preview"))
	s.maintainFileBriefings(context.WithoutCancel(ctx), briefing)
	slog.InfoContext(ctx, "file briefing preview retained", "project_id", briefing.ProjectID, "path", briefing.Path,
		"presentation", briefing.Presentation, "trigger", briefing.Trigger, "target_key", briefing.TargetKey)
}

func (s *Service) failFileBriefing(ctx context.Context, briefing Briefing, message string) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if ctx.Err() != nil || s.admissionError() != nil {
		return
	}
	now := time.Now().UTC()
	err := s.store.Fail(ctx, Outcome{
		ProjectID: briefing.ProjectID, RootID: briefing.RootID, Path: briefing.Path, TargetKey: briefing.TargetKey,
		AttemptID: briefing.AttemptID, Error: message, UpdatedAt: now,
	})
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			s.logger.WarnContext(ctx, "fail file briefing", "error", err)
		}
		return
	}
	briefing.Status, briefing.Error, briefing.UpdatedAt = StatusFailed, message, now
	s.publishFileBriefingEvent(context.WithoutCancel(ctx), fileBriefingEvent(briefing, "failed"))
	s.maintainFileBriefings(context.WithoutCancel(ctx), briefing)
}

func (s *Service) maintainFileBriefings(ctx context.Context, briefing Briefing) {
	if s.store == nil {
		return
	}
	if err := s.store.Maintain(
		ctx, briefing, s.config.Retention,
	); err != nil {
		s.logger.WarnContext(ctx, "maintain file briefings", "project_id", briefing.ProjectID,
			"root_id", briefing.RootID, "path", briefing.Path, "error", err)
	}
}

func fileBriefingJobKey(b Briefing) string {
	return b.ProjectID + "\x00" + b.RootID + "\x00" + b.Path + "\x00" + b.TargetKey + "\x00" + b.AttemptID
}

func (s *Service) publishFileBriefingEvent(ctx context.Context, event wire.FileBriefingEvent) {
	if s.events == nil {
		return
	}
	facet := event.RootID + "\x00" + event.Path + "\x00" + event.TargetKey
	if err := s.events.Publish(ctx, wire.EventTopicFileBriefing, events.PublishKey{Project: event.ProjectID, Facet: facet}, event); err != nil {
		s.logger.WarnContext(ctx, "publish file briefing", "error", err)
	}
}

func fileBriefingEvent(b Briefing, status string) wire.FileBriefingEvent {
	preview := fileBriefingPreviewDTO(b.Preview)
	return wire.FileBriefingEvent{
		ProjectID: b.ProjectID, TargetKey: b.TargetKey, AttemptID: b.AttemptID, RootID: b.RootID, Path: b.Path,
		Presentation: b.Presentation, SourceSHA256: b.SourceSHA256,
		Status: status, Preview: &preview, Locations: fileBriefingLocationsDTO(b.Locations),
		Sections: fileBriefingSectionsDTO(b.Sections), FallbackText: b.FallbackText,
		Truncated: b.Truncated, Error: b.Error,
		UpdatedAt: b.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func fileBriefingPreviewDTO(preview Preview) wire.FileBriefingPreview {
	return wire.FileBriefingPreview{Language: preview.Language, LineCount: preview.LineCount}
}

func fileBriefingLocationsDTO(locations []Location) []wire.FileBriefingLocation {
	out := make([]wire.FileBriefingLocation, 0, len(locations))
	for _, location := range locations {
		out = append(out, wire.FileBriefingLocation{Line: location.Line, Name: location.Name, Kind: location.Kind})
	}
	return out
}

func fileBriefingSectionsDTO(sections []Section) []wire.FileBriefingSection {
	out := make([]wire.FileBriefingSection, 0, len(sections))
	for _, section := range sections {
		out = append(out, wire.FileBriefingSection{Kind: section.Kind, Text: section.Text})
	}
	return out
}

type fileBriefingDeltaEmitter struct {
	context   context.Context
	service   *Service
	briefing  Briefing
	mu        sync.Mutex
	pending   strings.Builder
	emitted   bool
	lastFlush time.Time
}

func newFileBriefingDeltaEmitter(ctx context.Context, service *Service, briefing Briefing) *fileBriefingDeltaEmitter {
	return &fileBriefingDeltaEmitter{context: ctx, service: service, briefing: briefing}
}

func (e *fileBriefingDeltaEmitter) Add(ctx context.Context, delta string) {
	if delta == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pending.WriteString(delta)
	now := time.Now()
	if e.emitted && utf8.RuneCountInString(e.pending.String()) < e.service.config.Stream.ChunkChars && now.Sub(e.lastFlush) < e.service.config.StreamFlushInterval() {
		return
	}
	e.flushLocked(ctx, now)
}

func (e *fileBriefingDeltaEmitter) Flush(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.flushLocked(ctx, time.Now())
}

func (e *fileBriefingDeltaEmitter) flushLocked(ctx context.Context, now time.Time) {
	if e.pending.Len() == 0 {
		return
	}
	delta := e.pending.String()
	e.pending.Reset()
	e.service.gate.RLock()
	defer e.service.gate.RUnlock()
	if e.context.Err() != nil || e.service.admissionError() != nil {
		return
	}
	e.emitted, e.lastFlush = true, now
	event := fileBriefingEvent(e.briefing, "streaming")
	event.Preview, event.Locations, event.Sections = nil, nil, nil
	event.Delta = delta
	event.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	e.service.publishFileBriefingEvent(ctx, event)
}

func completedPartial(value string, minChars int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) < minChars {
		return ""
	}
	for i := len(value) - 1; i >= 0; i-- {
		switch value[i] {
		case '.', '!', '?':
			partial := strings.TrimSpace(value[:i+1])
			if utf8.RuneCountInString(partial) >= minChars {
				return partial
			}
		}
	}
	return ""
}
