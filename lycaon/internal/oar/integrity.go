package oar

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
)

// IntegrityReport summarizes load-time integrity checks.
type IntegrityReport struct {
	RuleCount           int
	ConditionsValidated int
	RegistryMissing     []string // hint codes absent from guidance_registry.json
	RegistryOrphans     []string // registry codes absent from hint catalog
	UnknownSpecVersions []string
}

// CheckIntegrity validates conditions and guidance registration.
func CheckIntegrity(hintsDir extpacks.Source, registryPath string, rules *RuleSet, cfg *guidance.HintConfig) (*IntegrityReport, error) {
	rep := &IntegrityReport{}
	hintCodes := map[string]struct{}{}
	if cfg != nil {
		for code := range cfg.HintCodes {
			hintCodes[code] = struct{}{}
		}
	} else if !hintsDir.Empty() {
		loaded, err := guidance.LoadHintConfig(hintsDir)
		if err != nil {
			return nil, err
		}
		for code := range loaded.HintCodes {
			hintCodes[code] = struct{}{}
		}
	}
	if rules != nil {
		rep.RuleCount = rules.Len()
		for _, r := range rules.All() {
			if r.When != "" {
				if err := checkWhenAgainstSpec(r.When, InstalledCapabilityDocument()); err != nil {
					return nil, fmt.Errorf("rule %s: when %q: %w", r.ID, r.When, err)
				}
				if err := ValidateConditionFactValues(r.When, hintCodes); err != nil {
					return nil, fmt.Errorf("rule %s: when %q: %w", r.ID, r.When, err)
				}
				rep.ConditionsValidated++
			}
			if r.OAR != SupportedSpecVersion {
				rep.UnknownSpecVersions = append(rep.UnknownSpecVersions, r.ID)
			}
		}
	}
	if len(rep.UnknownSpecVersions) > 0 {
		return rep, fmt.Errorf("unsupported spec_version on: %s", strings.Join(rep.UnknownSpecVersions, ", "))
	}

	regCodes, err := loadRegistryCodes(registryPath)
	if err != nil {
		return nil, err
	}
	if rules != nil {
		for _, r := range rules.All() {
			if _, ok := regCodes[r.ID]; !ok {
				rep.RegistryMissing = append(rep.RegistryMissing, r.ID)
			}
		}
	}
	for code := range regCodes {
		if _, ok := hintCodes[code]; !ok {
			rep.RegistryOrphans = append(rep.RegistryOrphans, code)
		}
	}
	return rep, nil
}

func loadRegistryCodes(path string) (map[string]struct{}, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read guidance_registry: %w", err)
	}
	var doc struct {
		HintCodes map[string]any `json:"hint_codes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse guidance_registry: %w", err)
	}
	out := make(map[string]struct{}, len(doc.HintCodes))
	for code := range doc.HintCodes {
		out[code] = struct{}{}
	}
	return out, nil
}

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
