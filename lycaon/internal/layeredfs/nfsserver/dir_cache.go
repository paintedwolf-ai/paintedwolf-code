package nfsserver

import (
	"container/list"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"path"
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

const defaultMaxDirSnapshotEntries = 1 << 20

type dirSnapshotEntry struct {
	Name     string
	Cookie   uint64
	QPath    layeredfs.QualifiedPath
	StableID layeredfs.NodeID
}

type dirCacheKey struct {
	hk   handleKey
	verf [8]byte
}

type dirSnapshot struct {
	key     dirCacheKey
	entries []dirSnapshotEntry
	lruElem *list.Element
}

// dirCache keeps one name list per listing pass, so a client paging with cookies sees each entry
// once. A cookie of zero always starts a fresh pass: a listing never repeats an old view of the
// directory. Attributes are read when a page is built, never stored here.
type dirCache struct {
	mu          sync.Mutex
	secret      [32]byte
	maxListings int
	maxEntries  int
	entries     int
	seq         uint64
	byKey       map[dirCacheKey]*dirSnapshot
	lru         *list.List
}

func newDirCache(secret [32]byte, maxListings int) *dirCache {
	if maxListings <= 0 {
		maxListings = DefaultMaxDirListings
	}
	return &dirCache{
		secret:      secret,
		maxListings: maxListings,
		maxEntries:  defaultMaxDirSnapshotEntries,
		byKey:       make(map[dirCacheKey]*dirSnapshot),
		lru:         list.New(),
	}
}

func (c *dirCache) nextVerifierLocked(hk handleKey) [8]byte {
	c.seq++
	var buf [24]byte
	binary.BigEndian.PutUint64(buf[0:8], hk.viewHash)
	binary.BigEndian.PutUint64(buf[8:16], uint64(hk.nodeID))
	binary.BigEndian.PutUint64(buf[16:24], c.seq)
	mac := hmac.New(sha256.New, c.secret[:])
	_, _ = mac.Write(buf[:])
	var verf [8]byte
	copy(verf[:], mac.Sum(nil)[:8])
	if verf == ([8]byte{}) {
		verf[7] = 1
	}
	return verf
}

func (c *dirCache) removeLocked(snap *dirSnapshot) {
	if snap.lruElem != nil {
		c.lru.Remove(snap.lruElem)
		snap.lruElem = nil
	}
	c.entries -= len(snap.entries)
	delete(c.byKey, snap.key)
}

// InvalidateDir ends every listing pass of a directory the server just mutated. A client holding an
// old cookie gets BAD_COOKIE and restarts rather than missing an entry.
func (c *dirCache) InvalidateDir(hk handleKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, snap := range c.byKey {
		if k.hk == hk {
			c.removeLocked(snap)
		}
	}
}

// ReleaseView purges all listing passes of a view.
func (c *dirCache) ReleaseView(viewID ViewID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	vHash := computeViewHash(viewID)
	for k, snap := range c.byKey {
		if k.hk.viewHash == vHash {
			c.removeLocked(snap)
		}
	}
}

// ReadSnapshot returns the entries after cookie and the verifier of their listing pass.
func (c *dirCache) ReadSnapshot(
	core *layeredfs.Core, dirRec *handleRecord, cookie uint64, clientVerf [8]byte,
) ([]dirSnapshotEntry, [8]byte, error) {
	var zeroVerf [8]byte
	if cookie != 0 {
		c.mu.Lock()
		defer c.mu.Unlock()
		snap, ok := c.byKey[dirCacheKey{hk: dirRec.key, verf: clientVerf}]
		if !ok || cookie > uint64(len(snap.entries)) {
			return nil, zeroVerf, ErrBadCookie
		}
		c.lru.MoveToFront(snap.lruElem)
		return snap.entries[cookie:], clientVerf, nil
	}
	built, err := buildDirEntries(core, dirRec)
	if err != nil {
		return nil, zeroVerf, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	verf := c.nextVerifierLocked(dirRec.key)
	snap := &dirSnapshot{key: dirCacheKey{hk: dirRec.key, verf: verf}, entries: built}
	snap.lruElem = c.lru.PushFront(snap)
	c.byKey[snap.key] = snap
	c.entries += len(built)
	for c.lru.Len() > 1 && (len(c.byKey) > c.maxListings || c.entries > c.maxEntries) {
		c.removeLocked(c.lru.Back().Value.(*dirSnapshot))
	}
	return built, verf, nil
}

func buildDirEntries(core *layeredfs.Core, dirRec *handleRecord) ([]dirSnapshotEntry, error) {
	rawEntries, err := core.ReadDir(dirRec.qpath)
	if err != nil {
		return nil, err
	}
	parentQPath := dirRec.qpath
	if cleanRel(dirRec.qpath.Rel) != "." {
		parentQPath = layeredfs.QualifiedPath{RootID: dirRec.qpath.RootID, Rel: cleanRel(path.Dir(dirRec.qpath.Rel))}
	}
	out := make([]dirSnapshotEntry, 0, len(rawEntries)+2)
	out = append(out,
		dirSnapshotEntry{Name: ".", Cookie: 1, QPath: dirRec.qpath, StableID: dirRec.key.nodeID},
		dirSnapshotEntry{Name: "..", Cookie: 2, QPath: parentQPath},
	)
	for i, ent := range rawEntries {
		childRel := ent.Name
		if cleanRel(dirRec.qpath.Rel) != "." {
			childRel = dirRec.qpath.Rel + "/" + ent.Name
		}
		out = append(out, dirSnapshotEntry{
			Name: ent.Name, Cookie: uint64(i + 3),
			QPath:    layeredfs.QualifiedPath{RootID: dirRec.qpath.RootID, Rel: childRel},
			StableID: ent.StableID(),
		})
	}
	return out, nil
}

func lookupRecordByKey(t *handleTable, hk handleKey) (*handleRecord, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	rec, ok := t.byKey[hk]
	if !ok {
		return nil, false
	}
	clone := *rec
	return &clone, true
}
