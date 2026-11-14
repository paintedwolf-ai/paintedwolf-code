package toolvocab

import (
	"regexp"
	"sort"
)

// handleToken matches an evidence handle as copy writes it: kind# or kind#N.
// A scoped handle such as leg-a:web#2 is excluded; it cites another agent's
// ledger, so the reader's own tools need not mint it.
var handleToken = regexp.MustCompile(`(?:^|[^A-Za-z0-9_/.:-])([a-z][a-z0-9_]*)#`)

var emptyHandle = regexp.MustCompile("`#[0-9N]*`")

// mintFacts maps every evidence kind to whether reachable tools mint it, the
// template fact copy gates handle names on.
func (c *Catalog) mintFacts(reachable map[string]bool) map[string]bool {
	minted, err := c.evidence.MintedKinds(reachable)
	if err != nil {
		return map[string]bool{}
	}
	return minted
}

// unmintableHandles lists the handles corpus names that no reachable tool or
// command operation mints, as "kind#". A tool name used as a handle is always
// listed: handles are named by evidence kind.
func (c *Catalog) unmintableHandles(corpus string, reachable map[string]bool) []string {
	minted := c.mintFacts(reachable)
	seen := map[string]bool{}
	var out []string
	// A handle rendered from an absent fact leaves only its marker.
	if emptyHandle.MatchString(corpus) {
		out = append(out, "#")
	}
	for _, m := range handleToken.FindAllStringSubmatch(corpus, -1) {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		ok, kind := minted[name]
		switch {
		case kind && !ok:
			out = append(out, name+"#")
		case !kind && (c.registered[name] || c.operations[name]):
			out = append(out, name+"#")
		}
	}
	sort.Strings(out)
	return out
}
