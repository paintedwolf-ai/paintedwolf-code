package progress

import (
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
)

var (
	progressGatedOnce  sync.Once
	progressGatedOrder []string
	progressGatedTools map[string]struct{}
	progressGatedErr   error
)

// InitProgressGatedTools loads the progress-gated tool catalog.
func InitProgressGatedTools(configRoot string) error {
	progressGatedOnce.Do(func() {
		progressGatedOrder, progressGatedTools, progressGatedErr = loadProgressGatedTools(configRoot)
	})
	return progressGatedErr
}

func loadProgressGatedTools(configRoot string) ([]string, map[string]struct{}, error) {
	data, err := readProgressGatedToolsBytes()
	if err != nil {
		return nil, nil, err
	}
	return parseProgressGatedToolsYAML(data)
}

func readProgressGatedToolsBytes() ([]byte, error) {
	data, err := config.Read(config.ProgressGatedTools)
	if err != nil {
		return nil, fmt.Errorf("read progress-gated tools: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, fmt.Errorf("progress-gated tools: empty file %s", config.ProgressGatedTools)
	}
	return data, nil
}

func parseProgressGatedToolsYAML(data []byte) ([]string, map[string]struct{}, error) {
	var raw struct {
		Tools []string `yaml:"tools"`
	}
	if err := config.DecodeYAML(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("parse progress-gated tools: %w", err)
	}
	order := make([]string, 0, len(raw.Tools))
	out := make(map[string]struct{}, len(raw.Tools))
	for _, name := range raw.Tools {
		name = strings.TrimSpace(strings.ToLower(name))
		if name == "" {
			continue
		}
		if _, seen := out[name]; !seen {
			order = append(order, name)
		}
		out[name] = struct{}{}
	}
	if len(out) == 0 {
		return nil, nil, fmt.Errorf("parse progress-gated tools: tools list is empty")
	}
	return order, out, nil
}

func progressGatedToolSet() map[string]struct{} {
	if progressGatedTools != nil {
		return progressGatedTools
	}
	_, set, err := loadProgressGatedTools("")
	if err != nil {
		panic(err)
	}
	return set
}

// GatedToolOrder returns the declared progress-gated tool and operation order.
func GatedToolOrder() []string {
	if progressGatedOrder != nil {
		return append([]string(nil), progressGatedOrder...)
	}
	order, _, err := loadProgressGatedTools("")
	if err != nil {
		panic(err)
	}
	return order
}

// IsProgressGatedTool reports whether name needs a coordinator progress checklist.
func IsProgressGatedTool(name string) bool {
	_, ok := progressGatedToolSet()[strings.TrimSpace(strings.ToLower(name))]
	return ok
}

// SurfaceMissingUpdateProgress reports whether tools include any progress-gated
// tool but omit AuthoringToolID (update_progress), using the same gated set as
// session host gating.
func SurfaceMissingUpdateProgress(tools []string) bool {
	gated := false
	hasAuthoring := false
	for _, tool := range tools {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			continue
		}
		if IsProgressGatedTool(tool) {
			gated = true
		}
		if tool == AuthoringToolID {
			hasAuthoring = true
		}
	}
	return gated && !hasAuthoring
}
