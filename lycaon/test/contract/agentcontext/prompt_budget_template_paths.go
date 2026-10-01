package contract

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
)

var pongoIncludeRE = regexp.MustCompile(`\{%\s*include\s+"([^"]+)"`)

func promptTemplateTrimPaths(lycaonRoot, rel string, extra ...string) []string {
	rel = strings.TrimSpace(strings.TrimPrefix(rel, "/"))
	if rel == "" {
		return append([]string(nil), extra...)
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(p string) {
		p = checkoutPath(lycaonRoot, strings.TrimSpace(p))
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	switch {
	case strings.HasPrefix(rel, "kicks/"), strings.HasPrefix(rel, "inject/"), strings.HasPrefix(rel, "guidance/"):
		stem := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(rel, "kicks/"), "inject/"), "guidance/")
		if path, err := catalogfixture.StockGuidancePath(stem); err == nil {
			add(path.String())
		}
	case strings.HasPrefix(rel, "agents/"):
		add(catalogfixture.StockAgentPromptTrimRel(strings.TrimPrefix(rel, "agents/")))
	default:
		add(catalogfixture.StockAgentPromptTrimRel(rel))
		abs, err := catalogfixture.StockAgentPromptPath(rel)
		if err == nil {
			raw, readErr := abs.Read()
			if readErr == nil {
				for _, m := range pongoIncludeRE.FindAllStringSubmatch(string(raw), -1) {
					if len(m) < 2 {
						continue
					}
					add(catalogfixture.StockAgentPromptTrimRel(m[1]))
				}
			}
		}
	}
	for _, p := range extra {
		add(p)
	}
	sort.Strings(out)
	return out
}

// checkoutPath renders a pack source path as its location in the checkout. A
// bundled Source.String() is relative to the embedded config filesystem, so it
// sits under lycaon/config rather than lycaon.
func checkoutPath(lycaonRoot, p string) string {
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		return filepath.ToSlash(filepath.Join("lycaon", "config", filepath.FromSlash(p)))
	}
	rel, err := filepath.Rel(lycaonRoot, p)
	if err != nil {
		return filepath.ToSlash(p)
	}
	return filepath.ToSlash(filepath.Join("lycaon", rel))
}

func kickTemplateIDs() ([]string, error) {
	packs, err := extpacks.DiscoverStock()
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var ids []string
	for _, dir := range extpacks.KindDirs(packs, "guidance") {
		entries, err := dir.List()
		if err != nil {
			return nil, err
		}
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".md") {
				continue
			}
			id := strings.TrimSuffix(ent.Name(), ".md")
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
