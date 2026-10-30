package toolcontract

import (
	"bytes"
	"encoding/json"
	"errors"
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
	Name               string `json:"name"`
	OperandsEndOptions bool   `json:"operands_end_options,omitempty"`
	// DefaultFormat is the output format used when no format option is given.
	DefaultFormat        string              `json:"default_format,omitempty"`
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

// OptionForm freezes spelling and consumption: -1 optional, -2 a value list
// its consumption terminates.
// Shorthand admits -VALUE for this arity-1 form: "numeric" (decimal digits) or
// "word" (any suffix whose first byte is not a declared short option).
type OptionForm struct {
	Spellings    []string `json:"spellings"`
	Arity        int      `json:"arity"`
	ValueRole    string   `json:"value_role"`
	FixtureID    string   `json:"fixture_id"`
	Scope        string   `json:"scope,omitempty"`
	Consumption  string   `json:"consumption"`
	Shorthand    string   `json:"shorthand,omitempty"`
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

// Help renders focused catalog help for one command specification.
func (c CommandSpecification) Help() string {
	var out strings.Builder
	out.WriteString(c.Name + "\n" + c.Argv + "\n")
	for _, form := range c.Forms {
		out.WriteString(strings.Join(form.Spellings, " | "))
		if form.Arity != 0 {
			out.WriteString(" " + form.ValueRole)
		}
		out.WriteByte('\n')
	}
	out.WriteString(c.Notes + "\n" + c.Encoding)
	return out.String()
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
		return command.Help(), true
	}
	return "", false
}

// DispositionReplaceRoutineSchema marks a tool whose routine use is a command form.
const DispositionReplaceRoutineSchema = "replace-routine-schema"

// ReplacedRoutineSchemas maps each tool whose routine schema the language
// replaces to the command form that replaces it.
func (c LanguageCatalog) ReplacedRoutineSchemas() map[string]string {
	out := map[string]string{}
	for _, tool := range c.Tools {
		if tool.Disposition == DispositionReplaceRoutineSchema {
			out[tool.Tool] = tool.Command
		}
	}
	return out
}

// DecodeLanguageJSON refuses unknown fields and trailing payloads in typed contracts.
func DecodeLanguageJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	switch err := decoder.Decode(new(any)); {
	case err == nil:
		return errors.New("trailing language JSON payload")
	case !errors.Is(err, io.EOF):
		return fmt.Errorf("trailing language JSON: %w", err)
	}
	return nil
}

func cloneStringSlice(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func cloneRawBytes(in json.RawMessage) json.RawMessage {
	if in == nil {
		return nil
	}
	out := make(json.RawMessage, len(in))
	copy(out, in)
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneRawMap(in map[string]json.RawMessage) map[string]json.RawMessage {
	if in == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		out[k] = cloneRawBytes(v)
	}
	return out
}

func cloneCommandSpecification(c CommandSpecification) CommandSpecification {
	c.Aliases = cloneStringSlice(c.Aliases)
	if c.Forms != nil {
		forms := make([]OptionForm, len(c.Forms))
		for i, f := range c.Forms {
			f.Spellings = cloneStringSlice(f.Spellings)
			f.SampleValues = cloneStringSlice(f.SampleValues)
			forms[i] = f
		}
		c.Forms = forms
	}
	c.Statuses = cloneStringMap(c.Statuses)
	if c.Effects != nil {
		effects := make([]EffectRequirement, len(c.Effects))
		for i, e := range c.Effects {
			e.Requires = cloneStringSlice(e.Requires)
			effects[i] = e
		}
		c.Effects = effects
	}
	c.PlatformCapabilities = cloneStringSlice(c.PlatformCapabilities)
	c.Limits = cloneStringSlice(c.Limits)
	c.Example = cloneRawBytes(c.Example)
	c.Reuse.Tests = cloneStringSlice(c.Reuse.Tests)
	c.Unsupported.Words = cloneStringSlice(c.Unsupported.Words)
	c.FixtureIDs = cloneStringSlice(c.FixtureIDs)
	return c
}

func cloneResourceLimits(in []ResourceLimit) []ResourceLimit {
	if in == nil {
		return nil
	}
	out := make([]ResourceLimit, len(in))
	for i, l := range in {
		l.BoundaryFixtures = cloneStringSlice(l.BoundaryFixtures)
		out[i] = l
	}
	return out
}

func cloneLanguageCatalog(c LanguageCatalog) LanguageCatalog {
	c.Language.Sources = cloneStringSlice(c.Language.Sources)
	c.Language.RequiredCommands = cloneStringSlice(c.Language.RequiredCommands)
	c.Language.RequiredContracts = cloneStringMap(c.Language.RequiredContracts)
	c.Language.Parser = cloneRawMap(c.Language.Parser)
	c.Language.Encoding = cloneStringMap(c.Language.Encoding)
	if c.Language.Syntax != nil {
		syntax := make(map[string][]string, len(c.Language.Syntax))
		for k, v := range c.Language.Syntax {
			syntax[k] = cloneStringSlice(v)
		}
		c.Language.Syntax = syntax
	}
	c.Language.Rules = cloneStringMap(c.Language.Rules)
	c.Language.Limits = cloneResourceLimits(c.Language.Limits)
	if c.Language.Diagnostics != nil {
		diagnostics := make([]LanguageDiagnostic, len(c.Language.Diagnostics))
		copy(diagnostics, c.Language.Diagnostics)
		c.Language.Diagnostics = diagnostics
	}
	c.Language.SourceLocations = cloneStringMap(c.Language.SourceLocations)
	c.Language.Compatibility = cloneStringMap(c.Language.Compatibility)
	c.Language.Platforms = cloneStringMap(c.Language.Platforms)
	c.Language.OptionGrammar = cloneStringMap(c.Language.OptionGrammar)
	if c.Language.SettlementReserve != nil {
		reserve := make(map[string]int64, len(c.Language.SettlementReserve))
		for k, v := range c.Language.SettlementReserve {
			reserve[k] = v
		}
		c.Language.SettlementReserve = reserve
	}
	if c.Commands != nil {
		commands := make([]CommandSpecification, len(c.Commands))
		for i, cmd := range c.Commands {
			commands[i] = cloneCommandSpecification(cmd)
		}
		c.Commands = commands
	}
	if c.Tools != nil {
		tools := make([]ToolDisposition, len(c.Tools))
		for i, t := range c.Tools {
			t.CommandForms = cloneStringSlice(t.CommandForms)
			t.ImplementingIssues = cloneStringSlice(t.ImplementingIssues)
			tools[i] = t
		}
		c.Tools = tools
	}
	c.Contracts = cloneRawMap(c.Contracts)
	if c.Fixtures != nil {
		fixtures := make([]FormFixture, len(c.Fixtures))
		for i, f := range c.Fixtures {
			f.Definition = cloneRawBytes(f.Definition)
			f.Words = cloneStringSlice(f.Words)
			fixtures[i] = f
		}
		c.Fixtures = fixtures
	}
	return c
}
