package conditions

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/blueprintfile"
)

// ShippedBlueprintDomainIDs returns Blueprint-content predicates registered at boot.
func ShippedBlueprintDomainIDs() []string {
	return []string{"blueprint_materialized", "options_selection_valid"}
}

// BlueprintMaterializedText requires authored content and a host-set lifecycle.
func BlueprintMaterializedText(content string) bool {
	meta, body := blueprintfile.SplitMarkdownFrontmatter(content)
	if strings.TrimSpace(body) == "" {
		return false
	}
	if raw, ok := meta["status"]; ok {
		switch strings.ToLower(strings.TrimSpace(fmt.Sprint(raw))) {
		case "approved", "implementing", "done":
			return false
		}
	}
	return true
}

// OptionsSelectionValidText validates the durable decision record Options asks
// the human to approve.
func OptionsSelectionValidText(content string) bool {
	if !BlueprintMaterializedText(content) {
		return false
	}
	meta, _ := blueprintfile.SplitMarkdownFrontmatter(content)
	for _, key := range []string{"criterion", "winner"} {
		if raw, ok := meta[key]; !ok || strings.TrimSpace(fmt.Sprint(raw)) == "" {
			return false
		}
	}
	return true
}

// RegisterBlueprintDomain registers content predicates shared by Blueprint-backed workflows.
func RegisterBlueprintDomain(reg *ConditionRegistry, deps RegistryDeps) error {
	if reg == nil {
		return nil
	}
	entries := []struct {
		name string
		fn   func(string) bool
	}{
		{name: "blueprint_materialized", fn: BlueprintMaterializedText},
		{name: "options_selection_valid", fn: OptionsSelectionValidText},
	}
	for _, entry := range entries {
		if reg.Has(entry.name) {
			continue
		}
		check := entry
		if err := reg.Register(check.name, func(ctx EvalContext) (bool, error) {
			content, ok := planContent(ctx, deps)
			return ok && check.fn(content), nil
		}); err != nil {
			return err
		}
	}
	return nil
}
