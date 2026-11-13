package toolvocab

import (
	"regexp"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// handleToken matches an evidence handle as copy writes it: kind# or kind#N.
// A scoped handle such as leg-a:web#2 is excluded; it cites another agent's
// ledger, so the reader's own tools need not mint it.
var handleToken = regexp.MustCompile(`(?:^|[^A-Za-z0-9_/.:-])([a-z][a-z0-9_]*)#`)

// handleIndex maps each evidence kind to the tools and native operations that
// mint it.
func handleIndex() (map[string][]string, error) {
	binding, err := evidence.LoadBinding()
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for kind := range binding.KindShapeMap() {
		out[kind] = nil
	}
	for _, tool := range binding.DeclaredToolNames() {
		for _, kind := range binding.ProducibleKinds(tool) {
			out[kind] = append(out[kind], tool)
		}
	}
	return out, nil
}

// unmintableHandles lists the handles corpus names that no reachable tool or
// command operation mints, as "kind#". A tool name used as a handle is always
// listed: handles are named by evidence kind.
func (c *Catalog) unmintableHandles(corpus string, reachable map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range handleToken.FindAllStringSubmatch(corpus, -1) {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		minters, kind := c.handleMinters[name]
		if !kind {
			if c.registered[name] || c.operations[name] {
				out = append(out, name+"#")
			}
			continue
		}
		if !c.mints(minters, reachable) {
			out = append(out, name+"#")
		}
	}
	sort.Strings(out)
	return out
}

func (c *Catalog) mints(minters []string, reachable map[string]bool) bool {
	for _, tool := range minters {
		tool = strings.TrimSpace(tool)
		if reachable[tool] {
			return true
		}
		if c.operations[tool] && toolcontract.CommandLanguageReachable(tool, reachable) {
			return true
		}
	}
	return false
}
