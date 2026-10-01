package observability

import (
	"log/slog"
	"sync"
)

// LazyComponent returns a logger that always uses the current slog.Default() with a component tag.
// Prefer this over package-init Component(slog.Default(), …) so serve-time SetDefault is respected.
func LazyComponent(name string) *lazyComponentLogger {
	return &lazyComponentLogger{name: name}
}

type lazyComponentLogger struct {
	name string
	mu   sync.Mutex
	log  *slog.Logger
}

func (l *lazyComponentLogger) logger() *slog.Logger {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.log == nil {
		l.log = Component(slog.Default(), l.name)
	}
	return l.log
}

func (l *lazyComponentLogger) Debug(msg string, args ...any) {
	l.logger().Debug(msg, args...)
}

func (l *lazyComponentLogger) Info(msg string, args ...any) {
	l.logger().Info(msg, args...)
}

func (l *lazyComponentLogger) Warn(msg string, args ...any) {
	l.logger().Warn(msg, args...)
}
