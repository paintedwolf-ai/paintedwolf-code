// Package contribution compiles contribution units into immutable sets.
package contribution

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Kind is one closed author-facing contribution kind.
type Kind string

const (
	KindCommand        Kind = "commands"
	KindMenu           Kind = "menus"
	KindKeybinding     Kind = "keybindings"
	KindEditorAction   Kind = "editor-actions"
	KindConfiguration  Kind = "configuration"
	KindMCPRequirement Kind = "mcp-requirements"
	KindSearchSource   Kind = "search-sources"
	KindOperation      Kind = "operations"
	KindTheme          Kind = "themes"
)

// Kinds returns every contribution kind in stable order.
func Kinds() []Kind {
	return []Kind{
		KindCommand, KindMenu, KindKeybinding,
		KindEditorAction, KindConfiguration, KindMCPRequirement,
		KindSearchSource, KindOperation,
		KindTheme,
	}
}

// UnitRoot is the pack inventory root that carries this kind.
func (k Kind) UnitRoot() string { return "contributions/" + string(k) }

// KindForUnitRoot maps an inventory root back to its contribution kind.
func KindForUnitRoot(root string) (Kind, bool) {
	for _, k := range Kinds() {
		if k.UnitRoot() == root {
			return k, true
		}
	}
	return "", false
}

// ID is `<provider-pack-id>:<name>`.
type ID struct {
	Provider string
	Name     string
}

func (id ID) String() string { return id.Provider + ":" + id.Name }

var nameGrammar = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ParseID validates a full contribution identifier.
func ParseID(raw string) (ID, error) {
	raw = strings.TrimSpace(raw)
	provider, name, ok := strings.Cut(raw, ":")
	if !ok {
		return ID{}, fmt.Errorf("contribution id %q must be <provider-pack-id>:<name>", raw)
	}
	if provider == "" || !strings.Contains(provider, "/") {
		return ID{}, fmt.Errorf("contribution id %q provider must be a full pack id", raw)
	}
	if !nameGrammar.MatchString(name) {
		return ID{}, fmt.Errorf("contribution id %q name must be lowercase kebab-case", raw)
	}
	return ID{Provider: provider, Name: name}, nil
}

// Input is one winning contribution unit selected by pack resolution.
type Input struct {
	UnitID         string
	Kind           Kind
	ProviderPackID string
	Body           []byte
	Origin         string
}

// Unit is one compiled contribution declaration.
type Unit struct {
	ID             ID
	Kind           Kind
	ProviderPackID string
	UnitID         string
	Body           []byte
	Origin         string
}

// Set is an immutable compiled contribution set.
type Set struct {
	byID  map[ID]Unit
	kinds map[Kind][]ID

	commands      map[ID]*Command
	menus         map[ID]*Menu
	keybindings   map[ID]*Keybinding
	editorActions map[ID]*EditorAction
	configuration map[ID]*ConfigurationProperty
	requirements  map[ID]*MCPRequirement
	searchSources map[ID]*SearchSource
	operations    map[ID]*Operation
	themes        map[ID]*Theme

	settings        map[ID]ConfigurationSetting
	bindingDefaults []BindingDefault
	notes           []Note
}

// ConfigurationSetting contains one property's effective value.
type ConfigurationSetting struct {
	Property *ConfigurationProperty
	Value    any
	// Default reports that no desired-state value applied.
	Default bool
}

// Settings returns every resolved configuration value, sorted by id.
func (s *Set) Settings() []ConfigurationSetting {
	if s == nil {
		return nil
	}
	ids := make([]ID, 0, len(s.settings))
	for id := range s.settings {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	out := make([]ConfigurationSetting, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.settings[id])
	}
	return out
}

// SettingsForPack returns one pack's values keyed by property name.
func (s *Set) SettingsForPack(packID string) map[string]any {
	if s == nil {
		return nil
	}
	out := map[string]any{}
	for id, setting := range s.settings {
		if id.Provider == packID {
			out[id.Name] = setting.Value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ConfigurationOn reads a compiled boolean property.
func (s *Set) ConfigurationOn(raw string) bool {
	if s == nil {
		return false
	}
	id, err := ParseID(raw)
	if err != nil {
		return false
	}
	setting, ok := s.settings[id]
	if !ok {
		return false
	}
	on, _ := setting.Value.(bool)
	return on
}

// Unit returns the declaration for id.
func (s *Set) Unit(id ID) (Unit, bool) {
	if s == nil {
		return Unit{}, false
	}
	u, ok := s.byID[id]
	return u, ok
}

// Len returns the total number of compiled declarations.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.byID)
}

// Fault codes are stable compile diagnostics.
const (
	FaultParse        = "contribution_parse"
	FaultIDMissing    = "contribution_id_missing"
	FaultIDInvalid    = "contribution_id_invalid"
	FaultNamespace    = "contribution_namespace"
	FaultNameMismatch = "contribution_name_mismatch"
	FaultDuplicateID  = "contribution_duplicate_id"
	FaultSchema       = "contribution_schema"
	FaultReference    = "contribution_reference"
	FaultAuthority    = "contribution_authority"
	FaultValue        = "contribution_value"
	FaultBounds       = "contribution_bounds"
)

// MaxDeclarationsPerKind bounds one pack's declarations by kind.
const MaxDeclarationsPerKind = 512

// Fault is one typed compile failure attributed to its declaring pack.
type Fault struct {
	PackID  string
	UnitID  string
	Code    string
	Message string
}

func (f Fault) Error() string {
	// Pack-level faults name no unit.
	if f.UnitID == "" {
		return fmt.Sprintf("%s: %s (pack %s)", f.Code, f.Message, f.PackID)
	}
	return fmt.Sprintf("%s: %s (%s, pack %s)", f.Code, f.Message, f.UnitID, f.PackID)
}

// CompileError rejects a candidate set. Faults name their packs for committed omission.
type CompileError struct {
	Faults []Fault
}

func (e *CompileError) Error() string {
	msgs := make([]string, 0, len(e.Faults))
	for _, f := range e.Faults {
		msgs = append(msgs, f.Error())
	}
	return "contribution compile: " + strings.Join(msgs, "; ")
}

// ProviderSeparator splits a contribution id into provider and name, and joins
// the provider into a provider-scoped unit id. ParseID cuts at the first one,
// so a name never contains it.
const ProviderSeparator = ":"

// container is the identity envelope. Kind schemas interpret the remaining
// fields; identity alone decides namespace and duplicates.
type container struct {
	ID string `yaml:"id"`
}

// compileIdentity enforces the identity rules for one unit.
func compileIdentity(in Input, fault func(Input, string, string)) (ID, bool) {
	doc, err := parseContainer(in.Body)
	if err != nil {
		fault(in, FaultParse, err.Error())
		return ID{}, false
	}
	if strings.TrimSpace(doc.ID) == "" {
		fault(in, FaultIDMissing, "declaration has no id")
		return ID{}, false
	}
	id, err := ParseID(doc.ID)
	if err != nil {
		fault(in, FaultIDInvalid, err.Error())
		return ID{}, false
	}
	if id.Provider != in.ProviderPackID {
		fault(in, FaultNamespace,
			fmt.Sprintf("id %s claims provider %s; identifiers belong to the declaring pack %s",
				id, id.Provider, in.ProviderPackID))
		return ID{}, false
	}
	if stem := unitStem(in.UnitID); stem != id.Name {
		fault(in, FaultNameMismatch,
			fmt.Sprintf("id name %q must match the unit file stem %q", id.Name, stem))
		return ID{}, false
	}
	return id, true
}

func parseContainer(body []byte) (container, error) {
	dec := yaml.NewDecoder(bytes.NewReader(body))
	var doc container
	if err := dec.Decode(&doc); err != nil {
		return container{}, fmt.Errorf("parse declaration: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return container{}, fmt.Errorf("parse declaration: multiple YAML documents are not allowed")
		}
		return container{}, fmt.Errorf("parse declaration: %w", err)
	}
	return doc, nil
}

// unitStem is the file stem a unit id was built from. A contribution unit id is
// its kind root followed by the declaration's identifier, so the stem is what
// follows the provider.
func unitStem(unitID string) string {
	if _, name, ok := strings.Cut(unitID, ProviderSeparator); ok {
		return name
	}
	if i := strings.LastIndex(unitID, "/"); i >= 0 {
		return unitID[i+1:]
	}
	return unitID
}
