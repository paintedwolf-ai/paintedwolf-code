package definition

import (
	"fmt"
	"strings"
)

// DepthLevel is a typed workflow depth parameter value.
type DepthLevel string

const (
	DepthNone     DepthLevel = "none"
	DepthLight    DepthLevel = "light"
	DepthThorough DepthLevel = "thorough"
)

// ParseDepthLevel normalizes a depth parameter value.
func ParseDepthLevel(raw string) (DepthLevel, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(DepthLight):
		return DepthLight, nil
	case string(DepthNone):
		return DepthNone, nil
	case string(DepthThorough):
		return DepthThorough, nil
	default:
		return "", fmt.Errorf("invalid depth level %q (want none|light|thorough)", raw)
	}
}

// WorkflowParameter describes one manifest parameters: entry.
type WorkflowParameter struct {
	Type    string
	Default string
}
