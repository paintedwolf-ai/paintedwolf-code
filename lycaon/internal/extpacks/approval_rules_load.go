package extpacks

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	wire "github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

const approvalRuleUnitPrefix = "approvals/rules/"

// ApprovalRuleUnit identifies one catalog approval rule and its pack.
type ApprovalRuleUnit struct {
	UnitID   string
	PackID   string
	Category wire.ApprovalCategory
	Pattern  string
	Effect   wire.ApprovalEffect
}

// LoadEffectiveApprovalRules parses loaded approval-rule units.
func LoadEffectiveApprovalRules(eff *EffectiveCatalog) ([]ApprovalRuleUnit, []Diagnostic, error) {
	if eff == nil {
		return nil, nil, fmt.Errorf("approval rules: effective catalog required")
	}
	var out []ApprovalRuleUnit
	var diags []Diagnostic
	for _, id := range eff.LoadedUnitIDs() {
		if !strings.HasPrefix(id, approvalRuleUnitPrefix) {
			continue
		}
		u := eff.Loaded[id]
		var body struct {
			Category wire.ApprovalCategory `yaml:"category"`
			Pattern  string                `yaml:"pattern"`
			Effect   wire.ApprovalEffect   `yaml:"effect"`
		}
		decoder := yaml.NewDecoder(bytes.NewReader(u.Content))
		decoder.KnownFields(true)
		if err := decoder.Decode(&body); err != nil {
			diags = append(diags, invalidApprovalRuleDiagnostic(id, u.WinnerPackID, err.Error()))
			continue
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				err = fmt.Errorf("multiple YAML documents are not allowed")
			}
			diags = append(diags, invalidApprovalRuleDiagnostic(id, u.WinnerPackID, err.Error()))
			continue
		}
		body.Pattern = strings.TrimSpace(body.Pattern)
		if err := validateApprovalRuleBody(body.Category, body.Pattern, body.Effect); err != nil {
			diags = append(diags, invalidApprovalRuleDiagnostic(id, u.WinnerPackID, err.Error()))
			continue
		}
		out = append(out, ApprovalRuleUnit{
			UnitID: id, PackID: u.WinnerPackID,
			Category: body.Category, Pattern: body.Pattern, Effect: body.Effect,
		})
	}
	if len(diags) > 0 {
		return nil, diags, fmt.Errorf("approvals/rules: %s", joinUnitDiagnostics(diags))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UnitID < out[j].UnitID })
	return out, nil, nil
}

func invalidApprovalRuleDiagnostic(id, packID, message string) Diagnostic {
	return Diagnostic{
		Code: DiagApprovalRuleInvalid, UnitID: id, PackID: packID,
		Message: fmt.Sprintf("invalid approval rule %s: %s", id, message),
	}
}

func validateApprovalRuleBody(category wire.ApprovalCategory, pattern string, effect wire.ApprovalEffect) error {
	switch category {
	case wire.ApprovalCategoryTool, wire.ApprovalCategoryCommand, wire.ApprovalCategoryMCP,
		wire.ApprovalCategoryPath, wire.ApprovalCategoryHost,
		wire.ApprovalCategoryWriteRoot, wire.ApprovalCategoryHostResource:
	default:
		return fmt.Errorf("unsupported category %q", category)
	}
	if pattern == "" {
		return fmt.Errorf("pattern is required")
	}
	if category == wire.ApprovalCategoryWriteRoot && !filepath.IsAbs(pattern) {
		return fmt.Errorf("write_root pattern must be an absolute directory")
	}
	switch effect {
	case wire.ApprovalEffectAsk, wire.ApprovalEffectDeny:
		return nil
	default:
		return fmt.Errorf("effect must be ask or deny, got %q", effect)
	}
}
