package packagegeneration

import (
	"fmt"
	"path"
	"strings"
)

// RuntimeLink preserves an observed input link, including alternatives-chain
// components outside selected roots. It grants no alias or executable authority.
type RuntimeLink struct {
	Path         string             `json:"path"`
	Target       string             `json:"target"`
	ResolvedPath string             `json:"resolved_path"`
	CapturedPath string             `json:"captured_path"`
	Origin       RuntimeOrigin      `json:"origin"`
	Observation  *KernelObservation `json:"observation"`
}

func (seal *RuntimeSeal) validateLinks(files map[string]RuntimeFile) error {
	seen := make(map[string]bool, len(seal.Links))
	aliases := make(map[string]RuntimeAlias, len(seal.Aliases))
	for _, alias := range seal.Aliases {
		aliases[alias.Path] = alias
	}
	for _, link := range seal.Links {
		// Raw Target is provenance, not a path to open or normalize. Absolute
		// targets, repeated separators and relative '..' retain their exact bytes.
		if !runtimeAbsolute(link.Path) || !runtimeAbsolute(link.ResolvedPath) ||
			!runtimeText(link.Target, 4095) || seen[link.Path] {
			return fmt.Errorf("runtime seal: invalid or duplicate input link %q", link.Path)
		}
		seen[link.Path] = true
		if err := link.Origin.validate(link.ResolvedPath); err != nil {
			return err
		}
		target, ok := files[link.CapturedPath]
		if !ok || target.Kind != "file" && target.Kind != "directory" {
			return fmt.Errorf("runtime seal: link target is not a retained physical object %q", link.Path)
		}
		mapped := false
		for name := link.ResolvedPath; name != "/"; name = path.Dir(name) {
			alias, exists := aliases[name]
			if !exists {
				continue
			}
			retained := ""
			switch {
			case alias.Path == link.ResolvedPath:
				retained = alias.Target
			case alias.Kind == "directory" && strings.HasPrefix(link.ResolvedPath, alias.Path+"/"):
				retained = path.Join(alias.Target, strings.TrimPrefix(link.ResolvedPath, alias.Path+"/"))
			default:
				continue
			}
			if retained != link.CapturedPath {
				return fmt.Errorf("runtime seal: link target differs from explicit projection %q", link.Path)
			}
			mapped = true
		}
		if !mapped {
			return fmt.Errorf("runtime seal: link target has no explicit projection %q", link.Path)
		}
	}
	return nil
}
