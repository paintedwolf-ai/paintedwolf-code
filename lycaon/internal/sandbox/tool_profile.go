package sandbox

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/config"
	"gopkg.in/yaml.v3"
)

// ToolProfile is a loaded tool permission profile.
type ToolProfile struct {
	ID          string
	Description string
	Tools       map[string]bool
	// StickyTools always load their schemas.
	StickyTools map[string]bool
	// DeferredTools load their schemas through request_tools.
	DeferredTools  map[string]bool
	DenyTools      []string
	MCPDeny        []string
	ReadGlobs      []string
	WriteGlobs     []string
	WaitConditions []string
}

// ToolDeferred reports whether toolName loads on demand. Exact entries win
// over patterns; sticky wins over deferred when both patterns match.
func (p ToolProfile) ToolDeferred(toolName string) bool {
	if p.ToolSticky(toolName) {
		return false
	}
	if p.DeferredTools[toolName] {
		return true
	}
	for pattern := range p.DeferredTools {
		if matchToolPattern(pattern, toolName) {
			return true
		}
	}
	return false
}

// ToolSticky reports whether toolName's schema is always present.
func (p ToolProfile) ToolSticky(toolName string) bool {
	if p.StickyTools[toolName] {
		return true
	}
	for pattern := range p.StickyTools {
		if matchToolPattern(pattern, toolName) {
			return true
		}
	}
	return false
}

// ToolDenied reports whether an explicit deny pattern removes toolName.
func (p ToolProfile) ToolDenied(toolName string) bool {
	for _, pattern := range p.DenyTools {
		if matchToolPattern(pattern, toolName) {
			return true
		}
	}
	return false
}

// ToolAllowed reports whether the profile's enumerated allowlist contains toolName.
func (p ToolProfile) ToolAllowed(toolName string) bool {
	if p.ToolDenied(toolName) {
		return false
	}
	if p.Tools[toolName] {
		return true
	}
	for pattern, allowed := range p.Tools {
		if allowed && matchToolPattern(pattern, toolName) {
			return true
		}
	}
	return false
}

// ToolMode controls when an allowed tool schema loads.
type ToolMode struct {
	Sticky bool
}

func (m *ToolMode) UnmarshalYAML(node *yaml.Node) error {
	var b bool
	if err := node.Decode(&b); err == nil {
		if !b {
			return fmt.Errorf("tools are denied by default — omit the entry instead of listing it false (use deny_tools to override an allow pattern)")
		}
		*m = ToolMode{}
		return nil
	}
	var s string
	if err := node.Decode(&s); err != nil || s != "sticky" {
		return fmt.Errorf("tool value must be \"sticky\" (schema upfront) or true (deferred)")
	}
	*m = ToolMode{Sticky: true}
	return nil
}

type toolProfileFile struct {
	ID             string              `yaml:"id"`
	Description    string              `yaml:"description"`
	Tools          map[string]ToolMode `yaml:"tools"`
	DenyTools      []string            `yaml:"deny_tools"`
	MCPDeny        []string            `yaml:"mcp_deny"`
	ReadScope      string              `yaml:"read_scope"`
	WriteScope     string              `yaml:"write_scope"`
	ReadScopes     []string            `yaml:"read_scopes"`
	WriteScopes    []string            `yaml:"write_scopes"`
	WaitConditions []string            `yaml:"wait_conditions"`
}

// ParseToolProfile parses profile YAML and resolves read_scope / write_scope via scopes.
func ParseToolProfile(data []byte, scopes PathScopeRegistry) (ToolProfile, error) {
	var raw toolProfileFile
	if err := config.DecodeYAML(data, &raw); err != nil {
		return ToolProfile{}, err
	}
	if raw.ID == "" {
		return ToolProfile{}, fmt.Errorf("tool profile missing id")
	}
	readGlobs, writeGlobs, err := resolveProfileScopes(raw, scopes)
	if err != nil {
		return ToolProfile{}, err
	}
	tools := make(map[string]bool, len(raw.Tools))
	var sticky, deferred map[string]bool
	for name, mode := range raw.Tools {
		tools[name] = true
		if mode.Sticky {
			if sticky == nil {
				sticky = make(map[string]bool)
			}
			sticky[name] = true
		} else {
			if deferred == nil {
				deferred = make(map[string]bool)
			}
			deferred[name] = true
		}
	}
	if deferred["request_tools"] {
		return ToolProfile{}, fmt.Errorf("profile %q: request_tools is the deferral recovery path and cannot itself defer — mark it sticky or omit it", raw.ID)
	}
	if err := validateWaitConditions(raw.ID, tools, raw.WaitConditions); err != nil {
		return ToolProfile{}, err
	}
	// Deferred profiles require request_tools.
	if len(deferred) > 0 && !tools["request_tools"] {
		tools["request_tools"] = true
		if sticky == nil {
			sticky = make(map[string]bool)
		}
		sticky["request_tools"] = true
	}
	return ToolProfile{
		ID:             raw.ID,
		Description:    raw.Description,
		Tools:          tools,
		StickyTools:    sticky,
		DeferredTools:  deferred,
		DenyTools:      raw.DenyTools,
		MCPDeny:        raw.MCPDeny,
		ReadGlobs:      readGlobs,
		WriteGlobs:     writeGlobs,
		WaitConditions: append([]string(nil), raw.WaitConditions...),
	}, nil
}

var supportedWaitConditions = map[string]struct{}{
	"next_worker_done": {}, "all_workers_idle": {}, "overlay_promote_pending": {},
	"scan_done": {}, "process_done": {},
	"http_ready": {}, "port_ready": {},
}

func validateWaitConditions(profileID string, tools map[string]bool, conditions []string) error {
	if len(conditions) > 0 && !tools["wait"] {
		return fmt.Errorf("profile %q declares wait_conditions without the wait tool", profileID)
	}
	seen := make(map[string]struct{}, len(conditions))
	for _, condition := range conditions {
		condition = strings.TrimSpace(condition)
		if _, ok := supportedWaitConditions[condition]; !ok {
			return fmt.Errorf("profile %q has unknown wait condition %q", profileID, condition)
		}
		if _, duplicate := seen[condition]; duplicate {
			return fmt.Errorf("profile %q repeats wait condition %q", profileID, condition)
		}
		seen[condition] = struct{}{}
	}
	return nil
}

func resolveProfileScopes(raw toolProfileFile, scopes PathScopeRegistry) (read, write []string, err error) {
	for _, id := range scopeIDList(raw.ReadScope, raw.ReadScopes) {
		r, _, e := ResolveScopeGlobs(scopes, id)
		if e != nil {
			return nil, nil, fmt.Errorf("profile %q read scope %q: %w", raw.ID, id, e)
		}
		read = append(read, r...)
	}
	for _, id := range scopeIDList(raw.WriteScope, raw.WriteScopes) {
		_, w, e := ResolveScopeGlobs(scopes, id)
		if e != nil {
			return nil, nil, fmt.Errorf("profile %q write scope %q: %w", raw.ID, id, e)
		}
		write = append(write, w...)
	}
	return read, write, nil
}

func scopeIDList(primary string, extra []string) []string {
	if primary == "" && len(extra) == 0 {
		return nil
	}
	ids := make([]string, 0, 1+len(extra))
	if primary != "" {
		ids = append(ids, primary)
	}
	ids = append(ids, extra...)
	return ids
}
