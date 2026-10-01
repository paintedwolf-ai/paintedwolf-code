// Package promptunit catalogs the loadable instruction units of a prompt: the
// prose a turn carries only when the request makes it relevant. A unit is a
// template under shared/units/ whose front matter says what request it serves
// (description), where it renders (slot), and which tools it follows
// (attaches). Floor prose is an ordinary include; only units are loadable.
package promptunit

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Slot is where a unit renders. Templates place `{{ units.<slot> }}` once per
// slot; loaded units render there in catalog order.
type Slot string

const (
	// SlotOrientation carries read ladders and tool procedures for finding
	// the way around a project.
	SlotOrientation Slot = "orientation"
	// SlotConduct carries rules that shape how work is done: security,
	// secrets, HTTP, visual evidence, git consent.
	SlotConduct Slot = "conduct"
	// SlotEvidence carries what counts as a receipt for a claim.
	SlotEvidence Slot = "evidence"
	// SlotExecution carries delegation, runner, and batching rules.
	SlotExecution Slot = "execution"
	// SlotProcedures carries per-call operating procedures for offered tools;
	// it renders in the tool procedures inject rather than the stable prompt.
	SlotProcedures Slot = "procedures"
)

// Slots lists every slot in render order.
func Slots() []Slot {
	return []Slot{SlotOrientation, SlotConduct, SlotEvidence, SlotExecution, SlotProcedures}
}

// Host is a prompt family a unit may render in.
type Host string

const (
	// HostCoordinator is the coordinator's tripartite prompt.
	HostCoordinator Host = "coordinator"
	// HostWorker is a worker persona.
	HostWorker Host = "worker"
)

// Unit is one loadable instruction unit.
type Unit struct {
	// ID is the unit stem, unique across packs.
	ID string
	// UnitID is the catalog id, shared/units/<stem>.
	UnitID string
	// Ref is the template ref, units/<stem>.
	Ref string
	PackID string
	// Stock is true for units the platform and bundled packs ship.
	Stock bool
	// Description says which requests need this unit; it is the only thing
	// the decision model reads about it.
	Description string
	Slot        Slot
	// Order sorts units within a slot, lowest first.
	Order int
	// Attaches names tools the unit follows: it renders only while one of
	// them is offered, and it follows their load decision when every one is
	// loadable on the surface.
	Attaches []string
	// NeededWith names tools whose call in a turn shows the unit was needed.
	// It labels the unit for the decision model and never changes rendering.
	NeededWith []string
	// Modes restricts the unit to execution-mode families; empty means any.
	Modes []string
	// Hosts lists the prompt families the unit renders in.
	Hosts []Host
}

// FrontMatter is the declared header of a unit template.
type FrontMatter struct {
	Description string   `yaml:"description"`
	Slot        string   `yaml:"slot"`
	Order       int      `yaml:"order"`
	Attaches    []string `yaml:"attaches"`
	NeededWith  []string `yaml:"needed_with"`
	Modes       []string `yaml:"modes"`
	Hosts       []string `yaml:"hosts"`
}

const (
	// DescriptionMaxWords bounds what the decision model reads per unit.
	DescriptionMaxWords = 60
	// DescriptionMaxRunes bounds the same text by length.
	DescriptionMaxRunes = 480
	// DefaultOrder is the order of a unit that declares none.
	DefaultOrder = 100
)

var (
	stemPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	knownModes  = map[string]bool{"investigate": true, "orchestrate": true, "wrapup": true}
)

// SplitFrontMatter separates a unit template into its YAML header and body.
// ok is false when the content has no leading fence pair.
func SplitFrontMatter(content []byte) (header, body []byte, ok bool) {
	content = bytes.TrimPrefix(content, []byte("\xEF\xBB\xBF"))
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], " \t") != "---" {
		return nil, nil, false
	}
	closeAt := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			closeAt = i
			break
		}
	}
	if closeAt < 0 {
		return nil, nil, false
	}
	header = []byte(strings.Join(lines[1:closeAt], "\n"))
	rest := strings.Join(lines[closeAt+1:], "\n")
	rest = strings.TrimLeft(rest, "\n")
	return header, []byte(rest), true
}

// Body returns the template text of a unit, without its header. Content
// without a header is returned unchanged.
func Body(content []byte) []byte {
	if _, body, ok := SplitFrontMatter(content); ok {
		return body
	}
	return content
}

// Parse validates one unit's front matter against its stem.
func Parse(stem string, content []byte) (Unit, error) {
	stem = strings.TrimSpace(stem)
	if !stemPattern.MatchString(stem) {
		return Unit{}, fmt.Errorf("unit %q: stem must be lowercase words joined by hyphens", stem)
	}
	header, _, ok := SplitFrontMatter(content)
	if !ok {
		return Unit{}, fmt.Errorf("unit %q: front matter fence pair required", stem)
	}
	dec := yaml.NewDecoder(bytes.NewReader(header))
	dec.KnownFields(true)
	var fm FrontMatter
	if err := dec.Decode(&fm); err != nil {
		return Unit{}, fmt.Errorf("unit %q: front matter: %w", stem, err)
	}
	desc := strings.Join(strings.Fields(fm.Description), " ")
	if desc == "" {
		return Unit{}, fmt.Errorf("unit %q: description is required", stem)
	}
	if words := len(strings.Fields(desc)); words > DescriptionMaxWords {
		return Unit{}, fmt.Errorf("unit %q: description has %d words; at most %d", stem, words, DescriptionMaxWords)
	}
	if n := utf8.RuneCountInString(desc); n > DescriptionMaxRunes {
		return Unit{}, fmt.Errorf("unit %q: description has %d characters; at most %d", stem, n, DescriptionMaxRunes)
	}
	slot := Slot(strings.TrimSpace(fm.Slot))
	if !knownSlot(slot) {
		return Unit{}, fmt.Errorf("unit %q: slot %q is not one of %s", stem, fm.Slot, slotNames())
	}
	order := fm.Order
	if order == 0 {
		order = DefaultOrder
	}
	if order < 0 {
		return Unit{}, fmt.Errorf("unit %q: order must not be negative", stem)
	}
	attaches, err := names(stem, "attaches", fm.Attaches)
	if err != nil {
		return Unit{}, err
	}
	neededWith, err := names(stem, "needed_with", fm.NeededWith)
	if err != nil {
		return Unit{}, err
	}
	for _, tool := range neededWith {
		if slices.Contains(attaches, tool) {
			return Unit{}, fmt.Errorf("unit %q: needed_with repeats attached tool %q", stem, tool)
		}
	}
	modes, err := names(stem, "modes", fm.Modes)
	if err != nil {
		return Unit{}, err
	}
	for _, mode := range modes {
		if !knownModes[mode] {
			return Unit{}, fmt.Errorf("unit %q: mode %q is not an execution mode family", stem, mode)
		}
	}
	hostNames, err := names(stem, "hosts", fm.Hosts)
	if err != nil {
		return Unit{}, err
	}
	if len(hostNames) == 0 {
		hostNames = []string{string(HostCoordinator)}
	}
	hosts := make([]Host, 0, len(hostNames))
	for _, h := range hostNames {
		switch Host(h) {
		case HostCoordinator, HostWorker:
			hosts = append(hosts, Host(h))
		default:
			return Unit{}, fmt.Errorf("unit %q: host %q is not coordinator or worker", stem, h)
		}
	}
	return Unit{
		ID:          stem,
		UnitID:      UnitIDPrefix + stem,
		Ref:         RefPrefix + stem,
		Description: desc,
		Slot:        slot,
		Order:       order,
		Attaches:    attaches,
		NeededWith:  neededWith,
		Modes:       modes,
		Hosts:       hosts,
	}, nil
}

const (
	// UnitIDPrefix is the catalog id prefix of a unit.
	UnitIDPrefix = "shared/units/"
	// RefPrefix is the template ref prefix of a unit.
	RefPrefix = "units/"
)

func names(stem, field string, in []string) ([]string, error) {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, name := range in {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("unit %q: %s contains an empty name", stem, field)
		}
		if seen[name] {
			return nil, fmt.Errorf("unit %q: %s repeats %q", stem, field, name)
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

func knownSlot(slot Slot) bool {
	for _, s := range Slots() {
		if s == slot {
			return true
		}
	}
	return false
}

func slotNames() string {
	out := make([]string, 0, len(Slots()))
	for _, s := range Slots() {
		out = append(out, string(s))
	}
	return strings.Join(out, ", ")
}

// HostsFor reports whether the unit renders in host.
func (u Unit) HostsFor(host Host) bool {
	for _, h := range u.Hosts {
		if h == host {
			return true
		}
	}
	return false
}

// ModeFits reports whether the unit renders in execution mode family mode.
func (u Unit) ModeFits(mode string) bool {
	if len(u.Modes) == 0 {
		return true
	}
	mode = strings.TrimSpace(mode)
	for _, m := range u.Modes {
		if m == mode {
			return true
		}
	}
	return false
}

// AttachedTo reports whether one of the unit's tools is in set.
func (u Unit) AttachedTo(set map[string]bool) bool {
	for _, tool := range u.Attaches {
		if set[tool] {
			return true
		}
	}
	return false
}

// Follows reports whether the unit follows its tools' load decision rather
// than carrying its own: it attaches to tools, and none of them is on the
// floor. A unit attached to a floor tool, or to nothing, is scored.
func (u Unit) Follows(floor map[string]bool) bool {
	return len(u.Attaches) > 0 && !u.AttachedTo(floor)
}

// Labelled reports whether a turn's tool calls can say if the unit was
// needed: it attaches to tools or names tools it is needed with.
func (u Unit) Labelled() bool {
	return len(u.Attaches) > 0 || len(u.NeededWith) > 0
}

// NeededBy reports whether a turn that called the tools in used needed the
// unit: one of its attached tools or one of its needed_with tools was called.
func (u Unit) NeededBy(used map[string]bool) bool {
	if u.AttachedTo(used) {
		return true
	}
	for _, tool := range u.NeededWith {
		if used[tool] {
			return true
		}
	}
	return false
}

// sortUnits orders units for rendering: slot order, then declared order,
// stock before other packs, then id.
func sortUnits(units []Unit) {
	rank := make(map[Slot]int, len(Slots()))
	for i, s := range Slots() {
		rank[s] = i
	}
	sort.SliceStable(units, func(i, j int) bool {
		a, b := units[i], units[j]
		if rank[a.Slot] != rank[b.Slot] {
			return rank[a.Slot] < rank[b.Slot]
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		if a.Stock != b.Stock {
			return a.Stock
		}
		return a.ID < b.ID
	})
}
