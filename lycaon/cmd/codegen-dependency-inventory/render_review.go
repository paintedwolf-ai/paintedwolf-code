package main

import (
	"fmt"
	"strings"
)

func renderConstraints(b *strings.Builder, sections []section, snap snapshot) {
	b.WriteString("## Upgrade constraints\n\nHeld packages, restricted update policies, and high-friction upgrades, even when current. " +
		"A blank Dependabot cell means default grouped updates; — means the entry is not managed by Dependabot.\n\n")
	var constrained []row
	for _, s := range sections {
		for _, r := range s.rows {
			if r.Friction == "high" || r.Updates != "" {
				constrained = append(constrained, r)
			}
		}
	}
	if len(constrained) == 0 {
		b.WriteString("None declared.\n\n")
		return
	}
	renderTable(b, constrained, snap, true)
}

func renderInventorySources(b *strings.Builder, sections []section) {
	b.WriteString("## Complete inventory sources\n\n" +
		"Policy files retain every declared entry, package rating, rationale, and verification note. " +
		"Manifests list all direct packages; lockfiles resolve transitive packages. " +
		"Declared runtime, engine, tool, and catalog pins are named in their policy files. " +
		"[Upstream snapshot](../../dependencies/upstream.json) records the queried versions.\n\n" +
		"| Area | Entries | Policy | Manifest and lockfile |\n|---|---|---|---|\n")
	for _, s := range sections {
		pins := "Declared pin sources in policy"
		if m := s.cfg.Manifest; m != nil {
			pins = fmt.Sprintf("[Manifest](../../%s)", m.Path)
			if m.Lock != "" {
				pins += fmt.Sprintf(" · [Lockfile](../../%s)", m.Lock)
			}
		}
		fmt.Fprintf(b, "| %s | %d | [Policy](../../dependencies/%s) | %s |\n",
			cell(s.cfg.Title), len(s.rows), s.cfg.file, pins)
	}
	b.WriteString("\n")
}
