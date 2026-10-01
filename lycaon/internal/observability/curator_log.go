package observability

import "log/slog"

func logCurator(ev CuratorEvent) {
	attrs := []any{
		slog.String("tool", ev.Tool),
		slog.String("view", ev.View),
		slog.String("target", ev.Target),
		slog.Int("selected", ev.Selected),
		slog.Int("total", ev.Total),
		slog.Int("dropped", ev.Dropped),
		slog.Int("retries", ev.Retries),
	}
	if ev.Fallback {
		attrs = append(attrs, slog.Bool("fallback", true))
	}
	if ev.CacheHit {
		attrs = append(attrs, slog.Bool("cache_hit", true))
	}
	if ev.SessionID != "" {
		attrs = append(attrs, slog.String("session_id", ev.SessionID))
	}
	slog.Info("tool_curator", attrs...)
}
