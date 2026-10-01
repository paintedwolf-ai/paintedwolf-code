package oar

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// Operator configuration controls rule enforcement ([OAR-CFG-1]–[OAR-CFG-8]).

var (
	configIDRE           = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	configQualifiedRefRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*\/[A-Z][A-Z0-9_]*$`)
	configVersionRE      = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

const (
	configMajor = 1
	configMinor = 0
)

// ProjectOperatorConfigBasename names project rule overrides.
const ProjectOperatorConfigBasename = "oar-config.yaml"

// ProjectOperatorConfigRel is that file's path under the overlay directory.
func ProjectOperatorConfigRel() string {
	return settingsoverlay.Rel(ProjectOperatorConfigBasename)
}

type configError struct{ message string }

func (e *configError) Error() string { return e.message }

func configErr(format string, args ...any) error {
	return &configError{message: fmt.Sprintf(format, args...)}
}

func (rs *RuleSet) lookupConfigRef(ref string) (*Rule, error) {
	if rs == nil {
		return nil, nil
	}
	for _, r := range rs.All() {
		if r.Qualified() == ref {
			return r, nil
		}
	}
	var matches []*Rule
	for _, r := range rs.All() {
		if r.ID == ref {
			matches = append(matches, r)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for _, r := range matches {
			ids = append(ids, r.Qualified())
		}
		return nil, configErr("[OAR-CFG-8] configuration names %s, which is ambiguous across namespaces: %v", ref, ids)
	}
	return nil, nil
}

func (rs *RuleSet) applyEnforcement(effective map[string]string) {
	if rs == nil || len(effective) == 0 {
		return
	}
	for _, r := range rs.All() {
		if v, ok := effective[r.Qualified()]; ok {
			r.Enforcement = v
		}
	}
}

func cloneRuleSet(rs *RuleSet) (*RuleSet, error) {
	if rs == nil {
		return nil, configErr("configuration requires a loaded rule set")
	}
	rules := make([]*Rule, 0, rs.Len())
	for _, original := range rs.All() {
		if original == nil {
			continue
		}
		clone := *original
		clone.errorTarget = nil
		rules = append(rules, &clone)
	}
	cloned := NewRuleSet(rules)
	if err := ResolveOverrides(cloned); err != nil {
		return nil, err
	}
	if err := RejectUnknownErrorSubstitutes(cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

// ConfiguredRuleSet applies operator configuration to an isolated rule set.
func ConfiguredRuleSet(docs []any, rs *RuleSet) (*RuleSet, error) {
	effective, err := resolveOperatorConfigs(docs, rs)
	if err != nil {
		return nil, err
	}
	configured, err := cloneRuleSet(rs)
	if err != nil {
		return nil, err
	}
	configured.applyEnforcement(effective)
	return configured, nil
}

func resolveOperatorConfigs(docs []any, rs *RuleSet) (map[string]string, error) {
	effective := map[string]string{}
	if rs == nil {
		return nil, configErr("configuration requires a loaded rule set")
	}
	for _, doc := range docs {
		obj, ok := doc.(map[string]any)
		if !ok {
			return nil, configErr("a configuration document must be a JSON object")
		}
		for key := range obj {
			if key != "oar_config" && key != "disable" && key != "enforcement" {
				return nil, configErr("[OAR-CFG-6] configuration document carries undefined field %q", key)
			}
		}
		version, ok := obj["oar_config"].(string)
		if !ok || !configVersionRE.MatchString(version) {
			return nil, configErr("configuration document field \"oar_config\" must be <major>.<minor>")
		}
		parts := strings.SplitN(version, ".", 2)
		major, _ := strconv.Atoi(parts[0])
		minor, _ := strconv.Atoi(parts[1])
		if major != configMajor {
			return nil, configErr("[OAR-CFG-1] configuration field \"oar_config\" %s names a major version this engine does not implement (%d.%d)", version, configMajor, configMinor)
		}
		if minor > configMinor {
			return nil, configErr("[OAR-CFG-1] configuration field \"oar_config\" %s names a minor version above this engine's own (%d.%d)", version, configMajor, configMinor)
		}

		disableRaw := obj["disable"]
		if disableRaw == nil {
			disableRaw = []any{}
		}
		disable, ok := disableRaw.([]any)
		if !ok {
			return nil, configErr("configuration field \"disable\" must be a list")
		}
		for _, raw := range disable {
			ref, ok := raw.(string)
			if !ok || (!configIDRE.MatchString(ref) && !configQualifiedRefRE.MatchString(ref)) {
				return nil, configErr("configuration field \"disable\" holds malformed rule reference %v", raw)
			}
			target, err := rs.lookupConfigRef(ref)
			if err != nil {
				return nil, err
			}
			if target == nil {
				return nil, configErr("[OAR-CFG-4] configuration disables %s, which is not loaded", ref)
			}
			if target.Mandatory {
				return nil, configErr("[OAR-CFG-5] configuration disables %s, whose mandatory is true", target.Qualified())
			}
			effective[target.Qualified()] = "off"
		}

		enforcementRaw := obj["enforcement"]
		if enforcementRaw == nil {
			enforcementRaw = map[string]any{}
		}
		enforcement, ok := enforcementRaw.(map[string]any)
		if !ok {
			return nil, configErr("configuration field \"enforcement\" must be an object")
		}
		for raw, value := range enforcement {
			if !configIDRE.MatchString(raw) && !configQualifiedRefRE.MatchString(raw) {
				return nil, configErr("configuration field \"enforcement\" holds malformed rule reference %q", raw)
			}
			s, ok := value.(string)
			if !ok || (s != "enforce" && s != "monitor" && s != "off") {
				return nil, configErr("configuration sets %s to %v, which is not an enforcement value", raw, value)
			}
			target, err := rs.lookupConfigRef(raw)
			if err != nil {
				return nil, err
			}
			if target == nil {
				return nil, configErr("[OAR-CFG-4] configuration sets enforcement for %s, which is not loaded", raw)
			}
			if target.Mandatory && s != "enforce" {
				return nil, configErr("[OAR-CFG-5] configuration downgrades %s to %s, whose mandatory is true", target.Qualified(), s)
			}
			effective[target.Qualified()] = s
		}
	}
	return effective, nil
}

// ConfiguredRuleSetBytes applies one encoded operator document.
func ConfiguredRuleSetBytes(rs *RuleSet, raw []byte) (*RuleSet, error) {
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, configErr("configuration document: %v", err)
	}
	return ConfiguredRuleSet([]any{doc}, rs)
}

// ConfiguredProjectRuleSet applies the project's operator document.
func ConfiguredProjectRuleSet(rs *RuleSet, projectDir string) (*RuleSet, error) {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return rs, nil
	}
	path := filepath.Join(projectDir, filepath.FromSlash(ProjectOperatorConfigRel()))
	raw, err := os.ReadFile(path) // #nosec G304 -- registered project root plus fixed relative path.
	if err != nil {
		if os.IsNotExist(err) {
			return rs, nil
		}
		return nil, err
	}
	return ConfiguredRuleSetBytes(rs, raw)
}
