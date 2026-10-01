package detectionpack

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/config"
)

const actionSemanticsFile = "detection-action-semantics.yaml"

// ActionSemantics is trusted catalog enrichment for a structured action. The
// catalog may only add review evidence; it never grants authority or suppresses
// a Sigma match.
type ActionSemantics struct {
	APIActions            []string
	ActionEffects         []string
	TargetScopes          []string
	PrincipalScopes       []string
	CredentialPersistence []string
	BulkAction            string
	AmountPresent         string
	TargetPresent         string
}

// ActionSemanticsCatalog is the compiled bundled+device mapping catalog.
type ActionSemanticsCatalog struct {
	mappings []actionMapping
	Warnings []string
}

type actionSemanticsDocument struct {
	Version  int                 `yaml:"version"`
	Mappings []actionMappingYAML `yaml:"mappings"`
}

type actionMappingYAML struct {
	ID                    string   `yaml:"id"`
	Description           string   `yaml:"description"`
	References            []string `yaml:"references"`
	ApprovalCategory      string   `yaml:"approval_category"`
	ApprovalSubjectRegex  string   `yaml:"approval_subject_re"`
	ToolRegex             string   `yaml:"tool_re"`
	APIActions            []string `yaml:"api_actions"`
	ActionEffects         []string `yaml:"action_effects"`
	TargetScopes          []string `yaml:"target_scopes"`
	PrincipalScopes       []string `yaml:"principal_scopes"`
	CredentialPersistence []string `yaml:"credential_persistence"`
	BulkAction            string   `yaml:"bulk_action"`
	AmountPresent         string   `yaml:"amount_present"`
	TargetPresent         string   `yaml:"target_present"`
	BulkArgs              []string `yaml:"bulk_args"`
	AmountArgs            []string `yaml:"amount_args"`
	TargetArgs            []string `yaml:"target_args"`
}

type actionMapping struct {
	actionMappingYAML
	subjectRE *regexp.Regexp
	toolRE    *regexp.Regexp
}

// DeviceActionSemanticsPath returns the additive device overlay path.
func DeviceActionSemanticsPath(configDir string) string {
	return filepath.Join(configDir, actionSemanticsFile)
}

// LoadActionSemantics loads the shipped mappings plus an optional additive
// device overlay. A malformed device file becomes a warning; malformed shipped
// data is a build/runtime error.
func LoadActionSemantics(configDir string) (*ActionSemanticsCatalog, error) {
	data, err := config.Read(config.DetectionActionSemantics)
	if err != nil {
		return nil, fmt.Errorf("read bundled detection action semantics: %w", err)
	}
	bundled, err := parseActionSemantics(data, "bundled")
	if err != nil {
		return nil, fmt.Errorf("parse bundled detection action semantics: %w", err)
	}
	cat := &ActionSemanticsCatalog{mappings: bundled}
	if strings.TrimSpace(configDir) == "" {
		return cat, nil
	}
	device, err := os.ReadFile(DeviceActionSemanticsPath(configDir)) // #nosec G304 -- device config root
	if errors.Is(err, os.ErrNotExist) {
		return cat, nil
	}
	if err != nil {
		cat.Warnings = append(cat.Warnings, fmt.Sprintf("read device action semantics: %v", err))
		return cat, nil
	}
	overlay, err := parseActionSemantics(device, "device")
	if err != nil {
		cat.Warnings = append(cat.Warnings, fmt.Sprintf("parse device action semantics: %v", err))
		return cat, nil
	}
	seen := map[string]struct{}{}
	for _, mapping := range cat.mappings {
		seen[mapping.ID] = struct{}{}
	}
	for _, mapping := range overlay {
		if _, exists := seen[mapping.ID]; exists {
			cat.Warnings = append(cat.Warnings, fmt.Sprintf("device action mapping %q collides with bundled mapping", mapping.ID))
			continue
		}
		seen[mapping.ID] = struct{}{}
		cat.mappings = append(cat.mappings, mapping)
	}
	return cat, nil
}

func parseActionSemantics(data []byte, source string) ([]actionMapping, error) {
	var document actionSemanticsDocument
	if err := config.DecodeYAML(data, &document); err != nil {
		return nil, err
	}
	if document.Version != 1 {
		return nil, fmt.Errorf("version=%d, want 1", document.Version)
	}
	seen := map[string]struct{}{}
	out := make([]actionMapping, 0, len(document.Mappings))
	for i, raw := range document.Mappings {
		mapping, err := compileActionMapping(raw)
		if err != nil {
			return nil, fmt.Errorf("%s mapping[%d]: %w", source, i, err)
		}
		if _, duplicate := seen[mapping.ID]; duplicate {
			return nil, fmt.Errorf("duplicate id %q", mapping.ID)
		}
		seen[mapping.ID] = struct{}{}
		out = append(out, mapping)
	}
	return out, nil
}

func compileActionMapping(raw actionMappingYAML) (actionMapping, error) {
	raw.ID = strings.TrimSpace(raw.ID)
	if raw.ID == "" || !packIDPattern.MatchString(raw.ID) {
		return actionMapping{}, fmt.Errorf("invalid id %q", raw.ID)
	}
	if strings.TrimSpace(raw.Description) == "" || len(raw.References) == 0 {
		return actionMapping{}, fmt.Errorf("%s requires description and references", raw.ID)
	}
	if strings.TrimSpace(raw.ApprovalCategory) == "" && strings.TrimSpace(raw.ApprovalSubjectRegex) == "" && strings.TrimSpace(raw.ToolRegex) == "" {
		return actionMapping{}, fmt.Errorf("%s requires an identity selector", raw.ID)
	}
	mapping := actionMapping{actionMappingYAML: raw}
	var err error
	if raw.ApprovalSubjectRegex != "" {
		if err := requireAnchoredIdentityRegex("approval_subject_re", raw.ApprovalSubjectRegex); err != nil {
			return actionMapping{}, fmt.Errorf("%s %w", raw.ID, err)
		}
		mapping.subjectRE, err = regexp.Compile(raw.ApprovalSubjectRegex)
		if err != nil {
			return actionMapping{}, fmt.Errorf("%s approval_subject_re: %w", raw.ID, err)
		}
	}
	if raw.ToolRegex != "" {
		if err := requireAnchoredIdentityRegex("tool_re", raw.ToolRegex); err != nil {
			return actionMapping{}, fmt.Errorf("%s %w", raw.ID, err)
		}
		mapping.toolRE, err = regexp.Compile(raw.ToolRegex)
		if err != nil {
			return actionMapping{}, fmt.Errorf("%s tool_re: %w", raw.ID, err)
		}
	}
	for field, values := range map[string][]string{
		"ActionEffect": raw.ActionEffects, "TargetScope": raw.TargetScopes,
		"PrincipalScope": raw.PrincipalScopes, "CredentialPersistence": raw.CredentialPersistence,
	} {
		if err := validateTypedSelectionValues(field, values); err != nil {
			return actionMapping{}, fmt.Errorf("%s: %w", raw.ID, err)
		}
	}
	for field, value := range map[string]string{
		"BulkAction": raw.BulkAction, "AmountPresent": raw.AmountPresent, "TargetPresent": raw.TargetPresent,
	} {
		if value != "" {
			if err := validateTypedSelectionValues(field, []string{value}); err != nil {
				return actionMapping{}, fmt.Errorf("%s: %w", raw.ID, err)
			}
		}
	}
	if len(raw.APIActions)+len(raw.ActionEffects)+len(raw.TargetScopes)+len(raw.PrincipalScopes)+len(raw.CredentialPersistence) == 0 &&
		raw.BulkAction == "" && raw.AmountPresent == "" && raw.TargetPresent == "" &&
		len(raw.BulkArgs)+len(raw.AmountArgs)+len(raw.TargetArgs) == 0 {
		return actionMapping{}, fmt.Errorf("%s contributes no facts", raw.ID)
	}
	return mapping, nil
}

func requireAnchoredIdentityRegex(field, expression string) error {
	normalized := strings.TrimSpace(expression)
	normalized = strings.TrimPrefix(normalized, "(?i)")
	if !strings.HasPrefix(normalized, "^") || !strings.HasSuffix(normalized, "$") {
		return fmt.Errorf("%s must anchor the complete registry identity", field)
	}
	return nil
}

// Resolve returns the union of every matching mapping. Mapping order cannot
// change the result; presence fields merge toward the more conservative fact.
func (c *ActionSemanticsCatalog) Resolve(tool, approvalCategory, approvalSubject string, args map[string]any) ActionSemantics {
	if c == nil {
		return ActionSemantics{}
	}
	var out ActionSemantics
	for _, mapping := range c.mappings {
		if !mapping.matches(tool, approvalCategory, approvalSubject) {
			continue
		}
		out.APIActions = append(out.APIActions, mapping.APIActions...)
		out.ActionEffects = append(out.ActionEffects, mapping.ActionEffects...)
		out.TargetScopes = append(out.TargetScopes, mapping.TargetScopes...)
		out.PrincipalScopes = append(out.PrincipalScopes, mapping.PrincipalScopes...)
		out.CredentialPersistence = append(out.CredentialPersistence, mapping.CredentialPersistence...)
		out.BulkAction = mergePresence(out.BulkAction, mapping.BulkAction, bulkPresence(args, mapping.BulkArgs))
		out.AmountPresent = mergePresence(out.AmountPresent, mapping.AmountPresent, argsPresence(args, mapping.AmountArgs))
		out.TargetPresent = mergePresence(out.TargetPresent, mapping.TargetPresent, argsPresence(args, mapping.TargetArgs))
	}
	out.APIActions = canonicalBounded(out.APIActions, 64)
	out.ActionEffects = ProjectConsequenceEnums("ActionEffect", out.ActionEffects, 32)
	out.TargetScopes = ProjectConsequenceEnums("TargetScope", out.TargetScopes, 16)
	out.PrincipalScopes = ProjectConsequenceEnums("PrincipalScope", out.PrincipalScopes, 16)
	out.CredentialPersistence = ProjectConsequenceEnums("CredentialPersistence", out.CredentialPersistence, 8)
	return out
}

func (m actionMapping) matches(tool, category, subject string) bool {
	if want := strings.TrimSpace(m.ApprovalCategory); want != "" && !strings.EqualFold(want, strings.TrimSpace(category)) {
		return false
	}
	if m.subjectRE != nil && !m.subjectRE.MatchString(strings.TrimSpace(subject)) {
		return false
	}
	return m.toolRE == nil || m.toolRE.MatchString(strings.TrimSpace(tool))
}

func argsPresence(args map[string]any, paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	for _, path := range paths {
		if value, ok := lookupArgPath(args, path); ok && valuePresent(value) {
			return PresenceTrue
		}
	}
	return PresenceFalse
}

func bulkPresence(args map[string]any, paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	found := false
	for _, path := range paths {
		value, ok := lookupArgPath(args, path)
		if !ok || !valuePresent(value) {
			continue
		}
		found = true
		switch typed := value.(type) {
		case []any:
			if len(typed) > 1 {
				return PresenceTrue
			}
		case []string:
			if len(typed) > 1 {
				return PresenceTrue
			}
		case map[string]any:
			if len(typed) > 1 {
				return PresenceTrue
			}
		}
	}
	if found {
		return PresenceFalse
	}
	return PresenceUnknown
}

func lookupArgPath(args map[string]any, path string) (any, bool) {
	var current any = args
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func valuePresent(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case []string:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	case bool:
		return typed
	default:
		return true
	}
}

func mergePresence(values ...string) string {
	rank := map[string]int{"": 0, PresenceFalse: 1, PresenceUnknown: 2, PresenceTrue: 3}
	best := ""
	for _, value := range values {
		value = ProjectPresence(value)
		if rank[value] > rank[best] {
			best = value
		}
	}
	return best
}

// MappingIDs returns deterministic diagnostics/test inventory.
func (c *ActionSemanticsCatalog) MappingIDs() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.mappings))
	for _, mapping := range c.mappings {
		out = append(out, mapping.ID)
	}
	sort.Strings(out)
	return out
}
