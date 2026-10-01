package toolcontract

import (
	"strings"

	"github.com/lycaon/lycaon/internal/nativemanifest"
)

// ResourcePresence reports which live resource handles a session currently has.
type ResourcePresence struct {
	CommandJobs bool
	Terminals   bool
	Pages       bool
	HeldCalls   bool
}

var familyOf = map[string]string{}

func init() {
	for id, members := range compiledFamilies {
		for _, member := range members {
			familyOf[member] = id
		}
	}
}

// IsCatalog reports whether name has a compiled stock contract.
func IsCatalog(name string) bool {
	_, ok := Lookup(strings.TrimSpace(name))
	return ok
}

// MutatesContent reports tools whose arguments carry model-authored file content.
func MutatesContent(name string) bool {
	_, ok := compiledMutatesContent[strings.TrimSpace(strings.ToLower(name))]
	return ok
}

// FamilyOf returns the control family that contains name.
func FamilyOf(name string) (string, bool) {
	id, ok := familyOf[strings.TrimSpace(name)]
	return id, ok
}

// ExpandFamilies grants every member of a family when any member is listed.
func ExpandFamilies(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, name := range names {
		add(name)
		if family, ok := FamilyOf(name); ok {
			for _, member := range compiledFamilies[family] {
				add(member)
			}
		}
	}
	return out
}

// WithCompanions adds each listed tool's declared companions after the list.
func WithCompanions(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, name := range names {
		add(name)
	}
	for _, name := range names {
		for _, companion := range compiledCompanions[strings.TrimSpace(name)] {
			add(companion)
		}
	}
	return out
}

// Implied returns the control tools required by live resources.
func Implied(presence ResourcePresence) []string {
	var facts []string
	if presence.CommandJobs {
		facts = append(facts, nativemanifest.ResourceFactCommandJobs)
	}
	if presence.Terminals {
		facts = append(facts, nativemanifest.ResourceFactTerminals)
	}
	if presence.Pages {
		facts = append(facts, nativemanifest.ResourceFactPages)
	}
	if presence.HeldCalls {
		facts = append(facts, nativemanifest.ResourceFactHeldCalls)
	}
	var out []string
	seen := map[string]struct{}{}
	for _, fact := range facts {
		for _, member := range compiledResourceImplied[fact] {
			if _, ok := seen[member]; ok {
				continue
			}
			seen[member] = struct{}{}
			out = append(out, member)
		}
	}
	return out
}

// AppendImplied expands listed names, then adds resource-implied control tools.
func AppendImplied(names []string, presence ResourcePresence) []string {
	out := ExpandFamilies(names)
	seen := make(map[string]struct{}, len(out))
	for _, name := range out {
		seen[name] = struct{}{}
	}
	for _, name := range Implied(presence) {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}
