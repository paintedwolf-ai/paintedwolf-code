package layeredfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"time"
)

// BaseCapture records the immutable captured base state for a mutated path (#1657, #1663).
type BaseCapture struct {
	Path        QualifiedPath     `json:"path"`
	Layer       LayerSource       `json:"layer"`
	Kind        EntryKind         `json:"kind"`
	Mode        fs.FileMode       `json:"mode,omitempty"`
	Size        int64             `json:"size,omitempty"`
	ModTime     time.Time         `json:"mod_time,omitempty"`
	SHA256      string            `json:"sha256,omitempty"`
	LinkTarget  string            `json:"link_target,omitempty"`
	Xattrs      map[string][]byte `json:"xattrs,omitempty"`
	Ino         uint64            `json:"ino,omitempty"`
	BackingFile string            `json:"-"`
	CapturedAt  time.Time         `json:"captured_at"`
}

// RegisterSeedBase records a pre-captured base for materialized views (#1658)
// without re-reading the live trunk.
func (c *Core) RegisterSeedBase(base BaseCapture) error {
	if err := c.barrier.enterMutation(); err != nil {
		return err
	}
	defer c.barrier.leaveMutation()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.released {
		return ErrViewReleased
	}
	base.Path.Rel = cleanRelPath(base.Path.Rel)
	if _, exists := c.state.basesByPath[base.Path]; !exists {
		c.state.basesByPath[base.Path] = cloneBaseCapture(base)
		if base.Kind == EntryRegular && base.Size > 0 {
			c.counters.capturedBaseBytes.Add(base.Size)
		}
	}
	return nil
}

// CapturedBase returns the retained immutable BaseCapture for a path, if one was recorded.
func (c *Core) CapturedBase(qpath QualifiedPath) (BaseCapture, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	qpath.Rel = cleanRelPath(qpath.Rel)
	b, ok := c.state.basesByPath[qpath]
	if !ok {
		return BaseCapture{}, false
	}
	return cloneBaseCapture(b), true
}

func cloneBaseCapture(b BaseCapture) BaseCapture {
	if len(b.Xattrs) > 0 {
		b.Xattrs = cloneXattrs(b.Xattrs)
	}
	return b
}

// ensureBaseCaptured ensures that qpath has an immutable BaseCapture before any mutation is published.
func ensureBaseCaptured(c *Core, qpath QualifiedPath) (BaseCapture, error) {
	qpath.Rel = cleanRelPath(qpath.Rel)
	c.mu.RLock()
	if existing, ok := c.state.basesByPath[qpath]; ok {
		c.mu.RUnlock()
		return cloneBaseCapture(existing), nil
	}
	c.mu.RUnlock()

	captured, err := captureFromLowerOrParent(c, qpath)
	if err != nil {
		return BaseCapture{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.released {
		return BaseCapture{}, ErrViewReleased
	}
	if existing, ok := c.state.basesByPath[qpath]; ok {
		if captured.BackingFile != "" {
			_ = unlinkBackingFile(int(c.backing.baseDirFile.Fd()), filepath.Base(captured.BackingFile))
		}
		return cloneBaseCapture(existing), nil
	}
	c.state.basesByPath[qpath] = captured
	if captured.Kind == EntryRegular && captured.Size > 0 {
		c.counters.capturedBaseBytes.Add(captured.Size)
	}
	return cloneBaseCapture(captured), nil
}

func captureFromLowerOrParent(c *Core, qpath QualifiedPath) (BaseCapture, error) {
	c.mu.RLock()
	if isMaskedByPrivateDeletionOrReplacementLocked(c, qpath) {
		c.mu.RUnlock()
		return BaseCapture{
			Path:       qpath,
			Layer:      LayerTrunk,
			Kind:       EntryAbsent,
			CapturedAt: time.Now(),
		}, nil
	}
	parentLayers := c.cfg.ParentLayers
	provider := c.roots.providers[qpath.RootID]
	maxRetries := c.cfg.MaxCaptureRetries
	retryHook := c.cfg.CaptureRetryHook
	c.mu.RUnlock()

	for _, pl := range parentLayers {
		pe, ok, err := pl.Lookup(qpath)
		if err != nil {
			return BaseCapture{}, err
		}
		if !ok {
			continue
		}
		if pe.Kind == EntryAbsent {
			return BaseCapture{
				Path:       qpath,
				Layer:      LayerParent,
				Kind:       EntryAbsent,
				CapturedAt: time.Now(),
			}, nil
		}
		return captureParentEntry(c, qpath, pl, pe)
	}

	if provider == nil {
		return BaseCapture{
			Path:       qpath,
			Layer:      LayerTrunk,
			Kind:       EntryAbsent,
			CapturedAt: time.Now(),
		}, nil
	}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		base, coherent, err := tryCaptureTrunkOnce(c, qpath, provider, attempt, retryHook)
		if err != nil {
			return BaseCapture{}, err
		}
		if coherent {
			return base, nil
		}
		lastErr = ErrSourceChangedDuringCapture
	}
	if lastErr == nil {
		lastErr = ErrSourceChangedDuringCapture
	}
	return BaseCapture{}, lastErr
}

func captureParentEntry(c *Core, qpath QualifiedPath, pl ParentLayer, pe ParentEntry) (BaseCapture, error) {
	base := BaseCapture{
		Path:       qpath,
		Layer:      LayerParent,
		Kind:       pe.Kind,
		Mode:       pe.Mode,
		Size:       pe.Size,
		ModTime:    pe.ModTime,
		SHA256:     pe.SHA256,
		LinkTarget: pe.LinkTarget,
		Xattrs:     cloneXattrs(pe.Xattrs),
		CapturedAt: time.Now(),
	}
	if pe.Kind != EntryRegular {
		return base, nil
	}
	rc, err := pl.OpenContent(qpath)
	if err != nil {
		return BaseCapture{}, err
	}
	defer func() { _ = rc.Close() }()
	baseName, basePath, sha, size, err := writeBaseStream(c, rc)
	if err != nil {
		return BaseCapture{}, err
	}
	if pe.SHA256 != "" && sha != pe.SHA256 {
		_ = unlinkBackingFile(int(c.backing.baseDirFile.Fd()), baseName)
		return BaseCapture{}, ErrSourceChangedDuringCapture
	}
	base.SHA256 = sha
	base.Size = size
	base.BackingFile = basePath
	return base, nil
}

func writeBaseStream(c *Core, r io.Reader) (string, string, string, int64, error) {
	c.mu.Lock()
	if c.state.released {
		c.mu.Unlock()
		return "", "", "", 0, ErrViewReleased
	}
	c.state.seq.nextStorageID++
	baseName := fmt.Sprintf("base-%08d", c.state.seq.nextStorageID)
	basePath := filepath.Join(c.backing.baseDir, baseName)
	baseDirFD := int(c.backing.baseDirFile.Fd())
	c.mu.Unlock()

	f, err := createExclusiveBackingFile(baseDirFD, baseName, basePath, 0o600)
	if err != nil {
		return "", "", "", 0, err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), r)
	syncErr := syncFD(int(f.Fd()))
	closeErr := f.Close()
	dirSyncErr := syncFD(baseDirFD)
	if err := errors.Join(copyErr, syncErr, closeErr, dirSyncErr); err != nil {
		_ = unlinkBackingFile(baseDirFD, baseName)
		return "", "", "", 0, err
	}
	return baseName, basePath, hex.EncodeToString(h.Sum(nil)), n, nil
}
