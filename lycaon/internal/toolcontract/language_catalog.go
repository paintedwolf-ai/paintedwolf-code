package toolcontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// LanguageCatalog is a fresh snapshot of the versioned contract, not execution authority.
// P01 publishes these facts; the later parser and operation binder consume them.
type LanguageCatalog struct {
	Version   int                        `json:"version"`
	Language  LanguageSpecification      `json:"language"`
	Commands  []CommandSpecification     `json:"commands"`
	Tools     []ToolDisposition          `json:"tools"`
	Contracts map[string]json.RawMessage `json:"contracts"`
	Fixtures  []FormFixture              `json:"fixtures"`
}

// LanguageSpecification freezes admission, representation and resource contracts.
type LanguageSpecification struct {
	Version           int                        `json:"version"`
	Sources           []string                   `json:"sources"`
	RequiredCommands  []string                   `json:"required_commands"`
	RequiredContracts map[string]string          `json:"required_contracts"`
	Parser            map[string]json.RawMessage `json:"parser"`
	Encoding          map[string]string          `json:"encoding"`
	Syntax            map[string][]string        `json:"syntax"`
	Rules             map[string]string          `json:"rules"`
	Limits            []ResourceLimit            `json:"limits"`
	Diagnostics       []LanguageDiagnostic       `json:"diagnostics"`
	SourceLocations   map[string]string          `json:"source_locations"`
	Compatibility     map[string]string          `json:"compatibility"`
	Platforms         map[string]string          `json:"platforms"`
	OptionGrammar     map[string]string          `json:"option_grammar"`
	Retention         string                     `json:"retention"`
	SettlementReserve map[string]int64           `json:"settlement_reserve"`
	SchemaCopy        string                     `json:"schema_copy"`
}

// LanguageDiagnostic identifies the typed refusal and its location/help contract.
type LanguageDiagnostic struct {
	ID       string `json:"id"`
	Location string `json:"location"`
	Help     string `json:"help"`
}

// CommandSpecification is the common source for parser admission and option help.
type CommandSpecification struct {
	Name                 string              `json:"name"`
	Aliases              []string            `json:"aliases"`
	Family               string              `json:"family"`
	Owner                string              `json:"owner"`
	Argv                 string              `json:"argv"`
	Forms                []OptionForm        `json:"forms"`
	Operands             string              `json:"operands"`
	Encoding             string              `json:"encoding"`
	Statuses             map[string]string   `json:"statuses"`
	Effects              []EffectRequirement `json:"effects"`
	PlatformCapabilities []string            `json:"platform_capabilities"`
	Limits               []string            `json:"limits"`
	Example              json.RawMessage     `json:"example"`
	Notes                string              `json:"notes"`
	Reuse                ReuseReference      `json:"reuse"`
	Unsupported          RejectionFixture    `json:"unsupported"`
	FixtureIDs           []string            `json:"fixture_ids"`
}

// RejectionFixture specifies a real refused operand/form, rather than treating all dash words as flags.
type RejectionFixture struct {
	Kind     string   `json:"kind"`
	Words    []string `json:"words"`
	Code     string   `json:"code"`
	Fallback bool     `json:"fallback"`
}

// OptionForm freezes spelling and consumption: -1 optional, -2 delimiter-terminated.
type OptionForm struct {
	Spellings    []string `json:"spellings"`
	Arity        int      `json:"arity"`
	ValueRole    string   `json:"value_role"`
	FixtureID    string   `json:"fixture_id"`
	Scope        string   `json:"scope,omitempty"`
	Consumption  string   `json:"consumption"`
	SampleValues []string `json:"sample_values"`
}

// EffectRequirement names a binder-selected mode, never a prose predicate.
type EffectRequirement struct {
	Mode     string   `json:"mode"`
	Requires []string `json:"requires"`
}

// ReuseReference separates existing operation evidence from new CLI qualification.
type ReuseReference struct {
	Implementation string   `json:"implementation"`
	Tests          []string `json:"tests"`
	Extension      string   `json:"extension"`
	EvidenceKind   string   `json:"evidence_kind"`
}

// ToolDisposition closes the cutover inventory over the stock manifest.
type ToolDisposition struct {
	CommandForms               []string `json:"command_forms"`
	Tool                       string   `json:"tool"`
	Owner                      string   `json:"owner"`
	Disposition                string   `json:"disposition"`
	Command                    string   `json:"command"`
	Lifecycle                  string   `json:"lifecycle"`
	Reuse                      string   `json:"reuse"`
	Source                     string   `json:"source"`
	Test                       string   `json:"test"`
	Cleanup                    string   `json:"cleanup"`
	SelectedDisposition        string   `json:"selected_disposition"`
	BehaviorChangeOrRetention  string   `json:"behavior_change_or_retention"`
	ExistingSemanticDifference string   `json:"existing_semantic_difference"`
	ImplementingIssues         []string `json:"implementing_issues"`
	ExistingRejectionCode      string   `json:"existing_rejection_code"`
}

// FormFixture is a golden admission fragment, independent from native runtime proof.
// Complete program/effect cases reside in Contracts and feed the later P02/P04 suites.
type FormFixture struct {
	Definition   json.RawMessage `json:"definition,omitempty"`
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Command      string          `json:"command,omitempty"`
	Words        []string        `json:"words,omitempty"`
	ExpectedForm string          `json:"expected_form,omitempty"`
	Arity        int             `json:"arity,omitempty"`
	Diagnostic   string          `json:"diagnostic,omitempty"`
	Limit        string          `json:"limit,omitempty"`
	Value        int64           `json:"value,omitempty"`
}

// DecodeLanguageCatalog returns detached facts so callers cannot mutate a shared catalog.
func DecodeLanguageCatalog(data []byte) (LanguageCatalog, error) {
	var result LanguageCatalog
	if err := DecodeLanguageJSON(data, &result); err != nil {
		return result, fmt.Errorf("decode command language: %w", err)
	}
	if result.Version != LanguageVersion || result.Language.Version != LanguageVersion {
		return result, fmt.Errorf("unsupported command language version %d", result.Version)
	}
	return result, nil
}

// CommandHelp derives focused help from the same facts consumed by admission.
func (c LanguageCatalog) CommandHelp(name string) (string, bool) {
	for _, command := range c.Commands {
		found := command.Name == name
		for _, alias := range command.Aliases {
			found = found || alias == name
		}
		if !found {
			continue
		}
		var out strings.Builder
		out.WriteString(command.Name + "\n" + command.Argv + "\n")
		for _, form := range command.Forms {
			out.WriteString(strings.Join(form.Spellings, " | "))
			if form.Arity != 0 {
				out.WriteString(" " + form.ValueRole)
			}
			out.WriteByte('\n')
		}
		out.WriteString(command.Notes + "\n" + command.Encoding)
		return out.String(), true
	}
	return "", false
}

// DecodeLanguageJSON refuses unknown fields and trailing payloads in typed contracts.
func DecodeLanguageJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing language JSON: %v", err)
	}
	return nil
}
