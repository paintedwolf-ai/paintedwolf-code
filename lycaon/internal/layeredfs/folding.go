package layeredfs

import (
	"path"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// NameFolding is how a root's source filesystem compares names. A folding source treats every
// spelling that folds to the same key as one name and keeps the spelling it was given.
type NameFolding struct {
	// Case means names differing only by letter case are one name.
	Case bool `json:"case,omitempty"`
	// Unicode means composed and decomposed forms of a name are one name.
	Unicode bool `json:"unicode,omitempty"`
}

// Folds reports whether any two different spellings can name one entry.
func (f NameFolding) Folds() bool { return f.Case || f.Unicode }

var caseFolder = cases.Fold()

// key is the form every spelling of one name shares under f.
func (f NameFolding) key(name string) string {
	if f.Unicode {
		name = norm.NFC.String(name)
	}
	if f.Case {
		name = caseFolder.String(name)
	}
	return name
}

// relKey folds every component of a root-relative path.
func (f NameFolding) relKey(rel string) string {
	if !f.Folds() || rel == "." {
		return rel
	}
	dir, name := path.Split(rel)
	if dir == "" {
		return f.key(name)
	}
	return f.relKey(path.Clean(dir)) + "/" + f.key(name)
}

// spellingIndex finds the stored spelling of a private path from any spelling that folds to it. Only
// folding roots have one.
type spellingIndex map[QualifiedPath]map[string]struct{}

func (s spellingIndex) add(key QualifiedPath, rel string) {
	set := s[key]
	if set == nil {
		set = map[string]struct{}{}
		s[key] = set
	}
	set[rel] = struct{}{}
}

func (s spellingIndex) remove(key QualifiedPath, rel string) {
	set := s[key]
	delete(set, rel)
	if len(set) == 0 {
		delete(s, key)
	}
}

func (c *Core) foldingOf(rootID string) NameFolding {
	return c.roots.byID[rootID].Folding
}

func (c *Core) spellingKey(qpath QualifiedPath) (QualifiedPath, bool) {
	folding := c.foldingOf(qpath.RootID)
	if !folding.Folds() {
		return QualifiedPath{}, false
	}
	return QualifiedPath{RootID: qpath.RootID, Rel: folding.relKey(qpath.Rel)}, true
}

// indexPath records qpath as a private path or deletion; unindexPath forgets it once neither holds.
// Every change to pathIndex and deletedPaths goes through these, so the spelling index never drifts.
func (c *Core) indexPath(qpath QualifiedPath) {
	if key, ok := c.spellingKey(qpath); ok {
		c.state.spellings.add(key, qpath.Rel)
	}
}

func (c *Core) unindexPath(qpath QualifiedPath) {
	if _, live := c.state.pathIndex[qpath]; live {
		return
	}
	if _, deleted := c.state.deletedPaths[qpath]; deleted {
		return
	}
	if key, ok := c.spellingKey(qpath); ok {
		c.state.spellings.remove(key, qpath.Rel)
	}
}

func (c *Core) setPrivatePath(qpath QualifiedPath, nid NodeID) {
	c.state.pathIndex[qpath] = nid
	c.indexPath(qpath)
}

func (c *Core) dropPrivatePath(qpath QualifiedPath) {
	delete(c.state.pathIndex, qpath)
	c.unindexPath(qpath)
}

func (c *Core) markDeleted(qpath QualifiedPath, state deletedState) {
	c.state.deletedPaths[qpath] = state
	c.indexPath(qpath)
}

func (c *Core) unmarkDeleted(qpath QualifiedPath) {
	delete(c.state.deletedPaths, qpath)
	c.unindexPath(qpath)
}

// rebuildSpellings indexes every private path and deletion, after state is loaded whole.
func (c *Core) rebuildSpellings() {
	c.state.spellings = spellingIndex{}
	for qpath := range c.state.pathIndex {
		c.indexPath(qpath)
	}
	for qpath := range c.state.deletedPaths {
		c.indexPath(qpath)
	}
}

// storedSpelling is the spelling the view holds for qpath: the live private path first, then a
// deletion, under any spelling the root folds to the same name. With neither, qpath stands as given.
func (c *Core) storedSpelling(qpath QualifiedPath) QualifiedPath {
	if _, ok := c.state.pathIndex[qpath]; ok {
		return qpath
	}
	if _, ok := c.state.deletedPaths[qpath]; ok {
		return qpath
	}
	key, ok := c.spellingKey(qpath)
	if !ok {
		return qpath
	}
	var deleted QualifiedPath
	for rel := range c.state.spellings[key] {
		candidate := QualifiedPath{RootID: qpath.RootID, Rel: rel}
		if _, live := c.state.pathIndex[candidate]; live {
			return candidate
		}
		deleted = candidate
	}
	if deleted.Rel != "" {
		return deleted
	}
	return qpath
}

// canonicalSpelling is the spelling a mutation records for qpath on a folding root: each component
// takes the private spelling the view already holds or, failing that, the lower tree's, so one name
// never becomes two entries. A component found in neither keeps the spelling given.
func (c *Core) canonicalSpelling(qpath QualifiedPath) QualifiedPath {
	c.mu.RLock()
	folding := c.foldingOf(qpath.RootID)
	provider := c.roots.providers[qpath.RootID]
	c.mu.RUnlock()
	if !folding.Folds() || qpath.Rel == "." {
		return qpath
	}
	parent := "."
	for _, name := range strings.Split(qpath.Rel, "/") {
		next := QualifiedPath{RootID: qpath.RootID, Rel: joinRel(parent, name)}
		c.mu.RLock()
		stored := c.storedSpelling(next)
		c.mu.RUnlock()
		if stored != next {
			parent = stored.Rel
			continue
		}
		if provider != nil {
			if lowerName, ok := lowerSpellingIn(provider, parent, name, folding); ok {
				name = lowerName
			}
		}
		parent = joinRel(parent, name)
	}
	return QualifiedPath{RootID: qpath.RootID, Rel: parent}
}

func lowerSpellingIn(provider LowerProvider, dir, name string, folding NameFolding) (string, bool) {
	entries, err := provider.ReadDir(dir)
	if err != nil {
		return "", false
	}
	want := folding.key(name)
	for _, entry := range entries {
		if entry.Name == name {
			return name, true
		}
	}
	for _, entry := range entries {
		if folding.key(entry.Name) == want {
			return entry.Name, true
		}
	}
	return "", false
}

// NameFolding is how the root rootID compares names.
func (c *Core) NameFolding(rootID string) NameFolding {
	return c.foldingOf(rootID)
}
