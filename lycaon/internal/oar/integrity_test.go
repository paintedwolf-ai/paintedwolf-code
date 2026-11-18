package oar

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oarcore"
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
				if err := checkWhenAgainstSpec(r.When, InstalledEngineDeclaration()); err != nil {
					return nil, fmt.Errorf("rule %s: when %q: %w", r.ID, r.When, err)
				}
				if err := ValidateConditionFactValues(r.When, hintCodes); err != nil {
					return nil, fmt.Errorf("rule %s: when %q: %w", r.ID, r.When, err)
				}
				rep.ConditionsValidated++
			}
			if r.SpecVersion != SupportedSpecVersion {
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

// checkWhenAgainstSpec compiles a condition against the installed declaration.
func checkWhenAgainstSpec(when string, cap *EngineDeclaration) error {
	if cap == nil {
		cap = InstalledEngineDeclaration()
	}
	if strings.TrimSpace(when) == "" {
		return nil
	}
	env, err := capabilityEnvironment(cap)
	if err != nil {
		return err
	}
	return oarcore.CheckCondition(when, env)
}
