package modelinfo

import (
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/tokenest"
)

// DefaultFallbackTrueWindow is used when YAML omits fallback_true_window.
const DefaultFallbackTrueWindow = 262144

// ModelContextWindowRule maps model-id substrings to a true context window.
type ModelContextWindowRule struct {
	Match         []string `yaml:"match"`
	ContextWindow int      `yaml:"context_window"`
	TextEncoding  string   `yaml:"text_encoding"`
}

// ModelContextWindows is the on-disk model-context-windows.yaml shape.
type ModelContextWindows struct {
	FallbackTrueWindow int                      `yaml:"fallback_true_window"`
	Windows            []ModelContextWindowRule `yaml:"windows"`
}

var (
	bundledWindowsOnce sync.Once
	bundledWindows     ModelContextWindows
	bundledWindowsErr  error
)

// DefaultModelContextWindows returns the bundled true-window table.
// Fail-closed: panics if the bundled file cannot be loaded.
func DefaultModelContextWindows() ModelContextWindows {
	bundledWindowsOnce.Do(func() {
		bundledWindows, bundledWindowsErr = LoadModelContextWindows()
	})
	if bundledWindowsErr != nil {
		panic(bundledWindowsErr)
	}
	return bundledWindows
}

// LoadModelContextWindows reads the static true-window table. Missing/invalid fails closed.
func LoadModelContextWindows() (ModelContextWindows, error) {
	data, err := config.Read(config.ModelContextWindow)
	if err != nil {
		return ModelContextWindows{}, fmt.Errorf("read model context windows: %w", err)
	}
	var doc ModelContextWindows
	if err := config.DecodeYAML(data, &doc); err != nil {
		return ModelContextWindows{}, fmt.Errorf("parse model context windows: %w", err)
	}
	if doc.FallbackTrueWindow <= 0 {
		doc.FallbackTrueWindow = DefaultFallbackTrueWindow
	}
	for i, rule := range doc.Windows {
		if _, err := tokenest.NewCounter(rule.TextEncoding); err != nil {
			return ModelContextWindows{}, fmt.Errorf("model context windows[%d]: %w", i, err)
		}
		if len(rule.Match) == 0 {
			return ModelContextWindows{}, fmt.Errorf("model context windows[%d]: empty match list", i)
		}
		if rule.ContextWindow <= 0 {
			return ModelContextWindows{}, fmt.Errorf("model context windows[%d]: context_window must be positive", i)
		}
	}
	return doc, nil
}

// Lookup returns the first matching rule's context_window for modelID.
func (w ModelContextWindows) Lookup(modelID string) (int, bool) {
	for _, rule := range w.Windows {
		if MatchFirstSubstring(modelID, rule.Match) {
			return rule.ContextWindow, true
		}
	}
	return 0, false
}

// TextEncoding returns a declared ordinary-text encoding, or an empty estimate fallback.
func (w ModelContextWindows) TextEncoding(modelID string) string {
	for _, rule := range w.Windows {
		if MatchFirstSubstring(modelID, rule.Match) {
			return rule.TextEncoding
		}
	}
	return ""
}
