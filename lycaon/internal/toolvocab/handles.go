package toolvocab

import "regexp"

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
