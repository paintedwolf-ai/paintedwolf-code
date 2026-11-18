package oar

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/guidance"
)

// SyncRegistryFromStock regenerates guidance_registry.json from the union of
// every shipped pack's policy/ dir (the same load path production uses).
func SyncRegistryFromStock(registryPath string) error {
	cfg, err := guidance.LoadHintConfigStock()
	if err != nil {
		return err
	}
	return SyncRegistryFromHintConfig(cfg, registryPath)
}

// SyncRegistryFromHintConfig derives hint_codes from pack YAML.
func SyncRegistryFromHintConfig(cfg *guidance.HintConfig, registryPath string) error {
	if cfg == nil {
		return fmt.Errorf("hint config is nil")
	}
	out := map[string]any{
		"registry_version":  "1",
		"envelope_messages": map[string]any{},
		"thresholds":        map[string]any{},
		"hint_codes":        map[string]any{},
	}
	hc := out["hint_codes"].(map[string]any)
	for code, entry := range cfg.HintCodes {
		row := map[string]any{}
		if entry.Emit != "" {
			row["emit"] = entry.Emit
		}
		if entry.Category != "" {
			row["category"] = entry.Category
		}
		if entry.Severity != "" {
			row["severity"] = entry.Severity
		}
		if entry.Instead != "" {
			row["branch_instruction"] = entry.Instead
		}
		if refs := referencesForRegistry(entry.References); len(refs) > 0 {
			row["references"] = refs
		}
		summary := entry.What
		if summary == "" {
			summary = entry.Message
		}
		if summary != "" {
			row["summary"] = summary
		}
		hc[code] = row
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(registryPath, raw, 0o600)
}

// referencesForRegistry threads OAR interop tags into guidance_registry.json
// (documentation/searchability only — no behavior).
func referencesForRegistry(refs map[string]any) map[string]any {
	if len(refs) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, key := range []string{"owasp_llm", "mitre_atlas"} {
		if v, ok := refs[key]; ok && v != nil {
			out[key] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
