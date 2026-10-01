package oar

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"path/filepath"
	"strings"
)

// PackSource is a named rule pack (tier:pack).
type PackSource struct {
	Name string // provenance source tag (pack name)
	Dir  string // directory of hint YAML
}

// TierLoad identifies built-in and extension-pack rule sources.
type TierLoad struct {
	BuiltinDir extpacks.Source
	Packs      []PackSource
}

// LoadTiers loads built-in and pack layers.
func (l *Loader) LoadTiers(tl TierLoad) (*RuleSet, error) {
	if l == nil {
		return nil, fmt.Errorf("oar loader not initialized")
	}
	var layers []*RuleSet
	if !tl.BuiltinDir.Empty() {
		rs, err := l.compileDir(tl.BuiltinDir, TierBuiltin, tl.BuiltinDir.String())
		if err != nil {
			return nil, fmt.Errorf("builtin: %w", err)
		}
		layers = append(layers, rs)
	}
	for _, p := range tl.Packs {
		if strings.TrimSpace(p.Dir) == "" {
			continue
		}
		name := strings.TrimSpace(p.Name)
		if name == "" {
			name = filepath.Base(p.Dir)
		}
		rs, err := l.compileDir(extpacks.OnDisk(p.Dir), TierPack, "pack:"+name)
		if err != nil {
			return nil, fmt.Errorf("pack %s: %w", name, err)
		}
		for _, r := range rs.All() {
			r.Source = "pack:" + name
		}
		layers = append(layers, rs)
	}
	return MergeTiered(layers...)
}

// MergeTiered combines provenance tiers without replacing document identities.
func MergeTiered(sets ...*RuleSet) (*RuleSet, error) {
	// Rule identity includes namespace and ID.
	byIdentity := map[RuleIdentity]*Rule{}
	for _, rs := range sets {
		if rs == nil {
			continue
		}
		for _, r := range rs.All() {
			if r == nil || r.ID == "" {
				continue
			}
			key := r.Identity()
			if prev, ok := byIdentity[key]; ok {
				return nil, fmt.Errorf("[OAR-DOC-9] duplicate rule identity %s (%s vs %s)", key, prev.Source, r.Source)
			}
			byIdentity[key] = r
		}
	}
	out := make([]*Rule, 0, len(byIdentity))
	for _, r := range byIdentity {
		out = append(out, r)
	}
	return linkRuleSet(NewRuleSet(out))
}

func (l *Loader) compileDir(dir extpacks.Source, tier Tier, source string) (*RuleSet, error) {
	entries, err := hintregistry.List(dir)
	if err != nil {
		return nil, err
	}
	return l.compileEntries(entries, tier, source)
}
