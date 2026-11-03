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

type dirSnapshotEntry struct {
	Name   string
	Cookie uint64
	QPath  layeredfs.QualifiedPath
	Meta   layeredfs.EntryAttr
}

type dirCacheKey struct {
	hk   handleKey
	verf [8]byte
}

type dirSnapshot struct {
	key     dirCacheKey
	verf    [8]byte
	entries []dirSnapshotEntry
	lruElem *list.Element
}

type dirCache struct {
	mu          sync.Mutex
	secret      [32]byte
	maxListings int
	listGens    map[handleKey]uint64
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
		listGens:    make(map[handleKey]uint64),
		byKey:       make(map[dirCacheKey]*dirSnapshot),
		lru:         list.New(),
	}
}

func (c *dirCache) currentListGenLocked(hk handleKey) uint64 {
	gen := c.listGens[hk]
	if gen == 0 {
		gen = 1
		c.listGens[hk] = gen
	}
	return gen
}

func (c *dirCache) computeVerifierLocked(hk handleKey, nodeGen uint64) [8]byte {
	listGen := c.currentListGenLocked(hk)
	var buf [32]byte
	binary.BigEndian.PutUint64(buf[0:8], hk.viewHash)
	binary.BigEndian.PutUint64(buf[8:16], uint64(hk.nodeID))
	binary.BigEndian.PutUint64(buf[16:24], nodeGen)
	binary.BigEndian.PutUint64(buf[24:32], listGen)

	mac := hmac.New(sha256.New, c.secret[:])
	_, _ = mac.Write(buf[:])
	sum := mac.Sum(nil)
	var verf [8]byte
	copy(verf[:], sum[:8])
	if verf == ([8]byte{}) {
		verf[7] = 1
	}
	return verf
}

// InvalidateDir bumps the listing generation for a directory and purges cached snapshots.
func (c *dirCache) InvalidateDir(hk handleKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listGens[hk] = c.currentListGenLocked(hk) + 1
	for k, snap := range c.byKey {
		if k.hk != hk {
			continue
		}
		if snap.lruElem != nil {
			c.lru.Remove(snap.lruElem)
			snap.lruElem = nil
		}
		delete(c.byKey, k)
	}
}

// ReleaseView purges all directory listing generations and snapshots for a view.
func (c *dirCache) ReleaseView(viewID ViewID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	vHash := computeViewHash(viewID)
	for hk := range c.listGens {
		if hk.viewHash == vHash {
			delete(c.listGens, hk)
		}
	}
	for k, snap := range c.byKey {
		if k.hk.viewHash != vHash {
			continue
		}
		if snap.lruElem != nil {
			c.lru.Remove(snap.lruElem)
			snap.lruElem = nil
		}
		delete(c.byKey, k)
	}
}

// ReadSnapshot validates the client's cookie and cookieverf, returning the remaining
// directory entries and the active 8-byte cookie verifier.
func (c *dirCache) ReadSnapshot(
	core *layeredfs.Core,
	dirRec *handleRecord,
	dirMeta layeredfs.EntryAttr,
	cookie uint64,
	clientVerf [8]byte,
) ([]dirSnapshotEntry, [8]byte, error) {
	c.mu.Lock()
	expectedVerf := c.computeVerifierLocked(dirRec.key, uint64(dirMeta.Generation))
	var zeroVerf [8]byte
	if (cookie != 0 || clientVerf != zeroVerf) && clientVerf != expectedVerf {
		c.mu.Unlock()
		return nil, zeroVerf, ErrBadCookie
	}
	ck := dirCacheKey{hk: dirRec.key, verf: expectedVerf}
	if snap, ok := c.byKey[ck]; ok {
		if snap.lruElem != nil {
			c.lru.MoveToFront(snap.lruElem)
		}
		entries := snap.entries
		c.mu.Unlock()
		if cookie > uint64(len(entries)) {
			return nil, zeroVerf, ErrBadCookie
		}
		return entries[cookie:], expectedVerf, nil
	}
	c.mu.Unlock()

	built, err := buildDirEntries(core, dirRec, dirMeta)
	if err != nil {
		return nil, zeroVerf, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	currentVerf := c.computeVerifierLocked(dirRec.key, uint64(dirMeta.Generation))
	if (cookie != 0 || clientVerf != zeroVerf) && clientVerf != currentVerf {
		return nil, zeroVerf, ErrBadCookie
	}
	if cookie > uint64(len(built)) {
		return nil, zeroVerf, ErrBadCookie
	}
	ck = dirCacheKey{hk: dirRec.key, verf: currentVerf}
	if existing, ok := c.byKey[ck]; ok {
		if existing.lruElem != nil {
			c.lru.MoveToFront(existing.lruElem)
		}
		return existing.entries[cookie:], currentVerf, nil
	}
	for len(c.byKey) >= c.maxListings && c.lru.Len() > 0 {
		back := c.lru.Back()
		if back == nil {
			break
		}
		evicted := back.Value.(*dirSnapshot)
		c.lru.Remove(back)
		evicted.lruElem = nil
		delete(c.byKey, evicted.key)
	}
	snap := &dirSnapshot{
		key:     ck,
		verf:    currentVerf,
		entries: built,
	}
	snap.lruElem = c.lru.PushFront(snap)
	c.byKey[ck] = snap
	return built[cookie:], currentVerf, nil
}

func buildDirEntries(
	core *layeredfs.Core,
	dirRec *handleRecord,
	dirMeta layeredfs.EntryAttr,
) ([]dirSnapshotEntry, error) {
	rawEntries, err := core.ReadDir(dirRec.qpath)
	if err != nil {
		return nil, err
	}
	parentQPath := dirRec.qpath
	if cleanRel(dirRec.qpath.Rel) != "." {
		parentRel := path.Dir(dirRec.qpath.Rel)
		parentQPath = layeredfs.QualifiedPath{RootID: dirRec.qpath.RootID, Rel: cleanRel(parentRel)}
	}
	parentMeta, err := core.Lstat(parentQPath)
	if err != nil {
		parentMeta = dirMeta
	}

	out := make([]dirSnapshotEntry, 0, len(rawEntries)+2)
	out = append(out,
		dirSnapshotEntry{Name: ".", Cookie: 1, QPath: dirRec.qpath, Meta: dirMeta},
		dirSnapshotEntry{Name: "..", Cookie: 2, QPath: parentQPath, Meta: parentMeta},
	)
	for i, ent := range rawEntries {
		childRel := ent.Name
		if cleanRel(dirRec.qpath.Rel) != "." {
			childRel = dirRec.qpath.Rel + "/" + ent.Name
		}
		childQPath := layeredfs.QualifiedPath{RootID: dirRec.qpath.RootID, Rel: childRel}
		childMeta, err := core.Lstat(childQPath)
		if err != nil {
			childMeta = layeredfs.EntryAttr{
				NodeID: ent.NodeID, Generation: ent.Generation, Layer: ent.Layer,
				Kind: ent.Kind, Mode: ent.Mode, Size: ent.Size,
				ModTime: ent.ModTime, ATime: ent.ModTime, CTime: ent.ModTime,
			}
		}
		out = append(out, dirSnapshotEntry{
			Name:   ent.Name,
			Cookie: uint64(i + 3),
			QPath:  childQPath,
			Meta:   childMeta,
		})
	}
	return out, nil
}
