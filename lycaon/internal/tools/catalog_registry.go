package tools

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/nativemanifest"
)

// ParseCatalogToolIDs returns the allowlisted catalog tool ids.
func ParseCatalogToolIDs() ([]string, error) {
	cfg, err := LoadToolsConfig()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(cfg.Tools))
	for _, id := range cfg.Tools {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// RegisteredToolSet returns a set of registered tool names from metadata.
func RegisteredToolSet(reg *DefaultRegistry) map[string]bool {
	out := make(map[string]bool)
	if reg == nil {
		return out
	}
	for _, meta := range reg.List() {
		out[meta.Name] = true
	}
	return out
}

// bootToolClaimNames are tools BootRegisteredToolSet treats as available beyond
// native-tools.yaml. Each must be registered before serve validates its surface.
var bootToolClaimNames = []string{
	"command", "task", "scan_pack", "scan_list", "scan_summary", "scan_query", "scan_compare",
	"workflow_compose", "workflow_compose_from_template", "workflow_persist",
	"workflow_catalog_summaries", "workflow_user_feedback", "ask_user", "workflow_advance", "fanout_plan", "submit_verdict",
	"wait",
	"pack_board",
	"web_search", "fetch_url",
}

// BootToolClaimNames returns a copy of the tools BootRegisteredToolSet treats as available beyond native-tools.yaml.
func BootToolClaimNames() []string {
	return append([]string(nil), bootToolClaimNames...)
}

// ValidateBootToolClaimsHonest fails when a boot claim is not registered on the registry.
func ValidateBootToolClaimsHonest(reg *DefaultRegistry) error {
	registered := RegisteredToolSet(reg)
	var missing []string
	for _, name := range bootToolClaimNames {
		if !registered[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("boot tool claims not registered: %s", strings.Join(missing, ", "))
	}
	return nil
}

// BootRegisteredToolSet returns the tool names registered at serve boot, read
// only from the manifest sections that hold tool names.
func BootRegisteredToolSet(reg *DefaultRegistry) (map[string]bool, error) {
	registered := RegisteredToolSet(reg)
	cfg, err := nativemanifest.Load()
	if err != nil {
		return nil, fmt.Errorf("native tools config: %w", err)
	}
	lists := [][]string{cfg.AllTools(), cfg.RequiresWorkerBranch, cfg.SpawnsProcess}
	for _, members := range cfg.Families {
		lists = append(lists, members)
	}
	for _, resourceTools := range cfg.ResourceImplied {
		lists = append(lists, resourceTools)
	}
	for _, list := range lists {
		for _, name := range list {
			if name = strings.TrimSpace(name); name != "" {
				registered[name] = true
			}
		}
	}
	for _, name := range bootToolClaimNames {
		registered[name] = true
	}
	return registered, nil
}

// ValidateCatalogAllowlistSync fails when an allowlisted id is not registered.
func ValidateCatalogAllowlistSync(registered map[string]bool) error {
	ids, err := ParseCatalogToolIDs()
	if err != nil {
		return err
	}
	var missing []string
	for _, id := range ids {
		if !registered[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("allowlisted tools not registered: %s", strings.Join(missing, ", "))
	}
	return nil
}
