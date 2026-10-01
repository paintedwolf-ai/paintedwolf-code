// Package pongoplain compiles and executes authored templates under one policy.
package pongoplain

import (
	"fmt"
	"io"

	"github.com/flosch/pongo2/v6"
)

// Resource ceilings support bundled prompts.
const (
	MaxSourceBytes      = 256 << 10
	MaxOutputBytes      = 1 << 20
	MaxContextBytes     = 2 << 20
	MaxContextNodes     = 16 << 10
	MaxCollectionItems  = 256
	MaxContextDepth     = 32
	MaxTemplateTags     = 4096
	MaxForNesting       = 2
	MaxCompositionFiles = 256
	MaxCompositionDepth = 12
)

func init() { pongo2.SetAutoescape(false) }

// Profile is the closed vocabulary of template capabilities.
type Profile uint8

const (
	// Isolated cannot load another template.
	Isolated Profile = iota + 1
	// Composed permits preflighted static dependencies.
	Composed
)

var commonBannedTags = []string{
	"block",     // inherited bodies bypass the output ceiling
	"cycle",     // mutates parse state across renders
	"extends",   // inherited bodies bypass the output ceiling
	"filter",    // buffers its body outside the output ceiling
	"ifchanged", // buffers content and mutates parse state
	"import",    // imports macros
	"lorem",     // synthesizes content
	"macro",     // permits recursive expansion
	"now",       // reads wall-clock state
	"spaceless", // buffers its body outside the output ceiling
	"ssi",       // bypasses the template loader
}

var bannedFilters = []string{
	"center",       // caller-selected allocation width
	"ljust",        // caller-selected allocation width
	"make_list",    // expands strings beyond the item ceiling
	"random",       // reads process randomness
	"rjust",        // caller-selected allocation width
	"split",        // expands strings beyond the item ceiling
	"stringformat", // caller-selected formatting width
}

func bansFor(profile Profile) ([]string, error) {
	bans := append([]string(nil), commonBannedTags...)
	switch profile {
	case Isolated:
		return append(bans, "include"), nil
	case Composed:
		return bans, nil
	default:
		return nil, fmt.Errorf("pongoplain: unknown profile %d", profile)
	}
}

// NewSet installs policy before parsing.
func NewSet(name string, loader pongo2.TemplateLoader, profile Profile) (*pongo2.TemplateSet, error) {
	if loader == nil {
		return nil, fmt.Errorf("pongoplain: set %q requires a loader", name)
	}
	bans, err := bansFor(profile)
	if err != nil {
		return nil, err
	}
	set := pongo2.NewSet(name, loader)
	for _, tag := range bans {
		if err := set.BanTag(tag); err != nil {
			return nil, fmt.Errorf("pongoplain: ban tag %q on set %q: %w", tag, name, err)
		}
	}
	for _, filter := range bannedFilters {
		if err := set.BanFilter(filter); err != nil {
			return nil, fmt.Errorf("pongoplain: ban filter %q on set %q: %w", filter, name, err)
		}
	}
	return set, nil
}

type denyLoader struct{}

func (denyLoader) Abs(_, name string) string { return name }
func (denyLoader) Get(path string) (io.Reader, error) {
	return nil, fmt.Errorf("pongoplain: isolated template requested file %q", path)
}

// Compile validates and parses one isolated body.
func Compile(source string) (*pongo2.Template, error) {
	if _, err := Inspect(source, Isolated); err != nil {
		return nil, err
	}
	// Each body has isolated mutable parse state.
	set, err := NewSet("painted-wolf-string", denyLoader{}, Isolated)
	if err != nil {
		return nil, err
	}
	tpl, err := set.FromString(source)
	if err != nil {
		return nil, fmt.Errorf("pongoplain: compile: %w", err)
	}
	return tpl, nil
}
