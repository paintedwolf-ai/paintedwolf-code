package layeredfs

import (
	"errors"
	"io"
	"io/fs"
	"os"
)

// ErrBaseTooLarge is a base body longer than the reader asked for.
var ErrBaseTooLarge = errors.New("layeredfs: base content exceeds the limit")

// ReadBase reads what a path held before the view changed it: the base a copy-up captured, or the
// lower layer where nothing was captured. For a materialized view the lower layer is its pristine
// tree, so the base never moves; a mounted view's lower layer is the live source. exists is false
// when the base holds no regular file at the path.
func (c *Core) ReadBase(qpath QualifiedPath, limit int64) (body []byte, exists bool, err error) {
	if err := c.barrier.enterOp(); err != nil {
		return nil, false, err
	}
	defer c.barrier.leaveOp()
	qpath.Rel = cleanRelPath(qpath.Rel)
	c.mu.RLock()
	captured, hasCapture := c.state.basesByPath[qpath]
	provider := c.roots.providers[qpath.RootID]
	c.mu.RUnlock()

	var reader io.ReadCloser
	switch {
	case hasCapture:
		if captured.Kind != EntryRegular || captured.BackingFile == "" {
			return nil, false, nil
		}
		f, err := os.Open(captured.BackingFile)
		if err != nil {
			return nil, false, err
		}
		reader = f
	case provider != nil:
		f, entry, err := provider.OpenRead(qpath.Rel)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if entry.Kind != EntryRegular {
			_ = f.Close()
			return nil, false, nil
		}
		reader = f
	default:
		return nil, false, nil
	}
	defer func() { _ = reader.Close() }()
	body, err = io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, true, err
	}
	if int64(len(body)) > limit {
		return nil, true, ErrBaseTooLarge
	}
	return body, true, nil
}

// HasBase reports whether the base holds an entry at the path, without reading it.
func (c *Core) HasBase(qpath QualifiedPath) (bool, error) {
	if err := c.barrier.enterOp(); err != nil {
		return false, err
	}
	defer c.barrier.leaveOp()
	qpath.Rel = cleanRelPath(qpath.Rel)
	c.mu.RLock()
	captured, hasCapture := c.state.basesByPath[qpath]
	provider := c.roots.providers[qpath.RootID]
	c.mu.RUnlock()
	if hasCapture {
		return captured.Kind != EntryAbsent, nil
	}
	if provider == nil {
		return false, nil
	}
	entry, err := provider.Lstat(qpath.Rel)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil && entry.Kind != EntryAbsent, err
}
