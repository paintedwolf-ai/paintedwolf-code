package observability

import (
	"fmt"
	"log/slog"
	"runtime/debug"
)

// GuardPanic recovers and logs panics on detached goroutines.
func GuardPanic(component string) {
	r := recover()
	if r == nil {
		return
	}
	LogRecoveredPanic(component, r)
}

// LogRecoveredPanic logs a recovered panic with its call stack and attributes.
func LogRecoveredPanic(component string, recovered any, attrs ...any) {
	args := []any{
		"component", component,
		"panic", fmt.Sprintf("%v", recovered),
		"stack", string(debug.Stack()),
	}
	slog.Error("recovered panic on detached goroutine", append(args, attrs...)...)
}
