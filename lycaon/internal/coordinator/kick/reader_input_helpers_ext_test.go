package kick_test

import (
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
)

// readerDeclaredOffer offers the visible names, or every profile tool for nil,
// that the profile does not defer or that loaded names.
func readerDeclaredOffer(profiles []sandbox.ToolProfile, profileID string, visible []string, loaded map[string]bool) []string {
	var profile sandbox.ToolProfile
	for _, p := range profiles {
		if p.ID == profileID {
			profile = p
		}
	}
	names := visible
	if names == nil {
		for name, ok := range profile.Tools {
			if ok && !strings.HasSuffix(name, "*") {
				names = append(names, name)
			}
		}
	}
	out := []string{}
	for _, name := range names {
		if !profile.ToolDeferred(name) || loaded[name] {
			out = append(out, name)
		}
	}
	return out
}
