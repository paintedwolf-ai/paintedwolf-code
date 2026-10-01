// Package toolvocab resolves tool names authored in pack YAML against the tool
// surface, and every remedy a rule or skill states against the profiles that can
// receive it. Both are load failures: an unresolvable name compiles to a clause
// that never matches, and an unreachable remedy leaves a blocked model no exit.
package toolvocab

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolschema"
)

// Catalog is the tool surface authored names must land in.
type Catalog struct {
	registered map[string]bool
	// runtimePrefixes name tools a connected server fills in later, so a pattern
	// under one resolves without a registered member.
	runtimePrefixes []string
	profiles        map[string]sandbox.ToolProfile
	profileIDs      []string
	provenance      Provenance
}

// NewCatalog builds the declared tool vocabulary.
//
// It answers whether a name exists on this host's surface, not whether the
// handler is wired in this process — ValidateBootToolClaimsHonest covers that.
// Every input is catalog data, so a pack's view can be built and rejected on its
// own before any runtime registry exists.
func NewCatalog(
	schemas *toolschema.Config,
	profiles []sandbox.ToolProfile,
	prov Provenance,
) (*Catalog, error) {
	if schemas == nil {
		return nil, fmt.Errorf("tool schemas required")
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("no tool profiles")
	}
	cfg, err := nativemanifest.Load()
	if err != nil {
		return nil, fmt.Errorf("native tool manifest: %w", err)
	}
	c := &Catalog{
		registered:      map[string]bool{},
		runtimePrefixes: cfg.RuntimeRegisteredPrefixes,
		profiles:        make(map[string]sandbox.ToolProfile, len(profiles)),
		provenance:      prov,
	}
	for _, list := range [][]string{cfg.AllTools(), cfg.HostProducedTools} {
		for _, name := range list {
			c.registered[strings.TrimSpace(name)] = true
		}
	}
	// A bundled schema declares the tool exists, wired or not. A pack's schema
	// only describes a tool the host already registers.
	var unknown []string
	for name := range schemas.Tools {
		name = strings.TrimSpace(name)
		if pack := prov.schemaPack(name); pack != "" && !c.registered[name] {
			unknown = append(unknown, fmt.Sprintf(
				"tools/schemas/%s: pack %s supplies a schema for %q, which this host does not register — "+
					"a schema unit describes a tool, it cannot add one", name, pack, name))
			continue
		}
		c.registered[name] = true
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, joinProblems(unknown)
	}
	for _, p := range profiles {
		if strings.TrimSpace(p.ID) == "" {
			return nil, fmt.Errorf("tool profile with empty id")
		}
		c.profiles[p.ID] = p
		c.profileIDs = append(c.profileIDs, p.ID)
	}
	sort.Strings(c.profileIDs)
	return c, nil
}

// KnownName reports whether an exact tool name resolves. A runtime prefix
// resolves too — the host can produce it once a server connects.
func (c *Catalog) KnownName(name string) bool {
	name = strings.TrimSpace(name)
	if c.registered[name] {
		return true
	}
	for _, prefix := range c.runtimePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// KnownPattern reports whether an allow/deny entry resolves. A trailing-star
// pattern must match a registered tool or overlap a runtime prefix.
func (c *Catalog) KnownPattern(pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if !strings.HasSuffix(pattern, "*") {
		return c.KnownName(pattern)
	}
	prefix := strings.TrimSuffix(pattern, "*")
	for _, runtime := range c.runtimePrefixes {
		if strings.HasPrefix(prefix, runtime) || strings.HasPrefix(runtime, prefix) {
			return true
		}
	}
	for name := range c.registered {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// ProfileHolds reports whether profileID can call tool.
func (c *Catalog) ProfileHolds(profileID, tool string) bool {
	prof, ok := c.profiles[strings.TrimSpace(profileID)]
	if !ok {
		return false
	}
	return prof.ToolAllowed(strings.TrimSpace(tool))
}

// ProfileIDs returns every loaded profile id, sorted.
func (c *Catalog) ProfileIDs() []string {
	return append([]string(nil), c.profileIDs...)
}

// HasProfile reports whether profileID is loaded.
func (c *Catalog) HasProfile(profileID string) bool {
	_, ok := c.profiles[strings.TrimSpace(profileID)]
	return ok
}

// lacking returns audience members that cannot call tool, sorted.
func (c *Catalog) lacking(audience []string, tool string) []string {
	var out []string
	for _, id := range audience {
		if !c.ProfileHolds(id, tool) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// ValidateProfiles checks every allow key and deny pattern resolves. A deny that
// matches nothing reads as a carve-out and grants.
func ValidateProfiles(c *Catalog, profiles []sandbox.ToolProfile) error {
	var problems []string
	for _, p := range profiles {
		for _, name := range sortedKeys(p.Tools) {
			if !c.KnownPattern(name) {
				problems = append(problems, fmt.Sprintf(
					"profile %q grants %q, which resolves to no registered tool", p.ID, name))
			}
		}
		for _, pattern := range p.DenyTools {
			if !c.KnownPattern(pattern) {
				problems = append(problems, fmt.Sprintf(
					"profile %q denies %q, which matches no registered tool — a deny that matches nothing grants",
					p.ID, pattern))
			}
		}
	}
	return joinProblems(problems)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func joinProblems(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("%s", strings.Join(problems, "\n"))
}
