package contentblob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
)

// orphanCursor retains only the current directory handles between bounded passes.
type orphanCursor struct {
	projects *os.File
	objects  *os.File
	project  string
	shard    int
}

func (c *orphanCursor) close() {
	if c.projects != nil {
		_ = c.projects.Close()
		c.projects = nil
	}
	if c.objects != nil {
		_ = c.objects.Close()
		c.objects = nil
	}
}

func (c *orphanCursor) batch(ctx context.Context, deps GCDeps) error {
	lifecycle := bloblifecycle.ForDevice(deps.DataDir)
	if !lifecycle.TryLock() {
		return nil
	}
	defer lifecycle.Unlock()
	if err := deps.Guard.Verify(); err != nil {
		return err
	}
	if c.projects == nil {
		var err error
		c.projects, err = os.Open(filepath.Join(deps.DataDir, "projects"))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	if c.project == "" {
		entries, err := c.projects.ReadDir(1)
		if errors.Is(err, io.EOF) {
			c.close()
			return nil
		}
		if err != nil {
			return err
		}
		if len(entries) == 0 || !entries[0].IsDir() {
			return nil
		}
		c.project = entries[0].Name()
		c.shard = 0
	}
	if c.objects == nil {
		var err error
		c.objects, err = os.Open(filepath.Join(StoreFor(deps.DataDir, c.project).Root, Dir, fmt.Sprintf("%02x", c.shard)))
		if errors.Is(err, os.ErrNotExist) {
			c.nextShard()
			return nil
		}
		if err != nil {
			return err
		}
	}
	entries, err := c.objects.ReadDir(128)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			continue
		}
		sha := fmt.Sprintf("%02x", c.shard) + entry.Name()
		rel, err := RelPath(sha)
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if time.Since(info.ModTime()) < time.Hour {
			continue
		}
		var stored bool
		if err := deps.Database.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM content_blob_objects WHERE project_id = ? AND sha256 = ?)`, c.project, sha).Scan(&stored); err != nil {
			return err
		}
		if stored {
			continue
		}
		if _, err := StoreFor(deps.DataDir, c.project).RemoveAtBefore(rel, time.Now().Add(-time.Hour)); err != nil {
			return err
		}
	}
	if len(entries) < 128 {
		c.nextShard()
	}
	return nil
}

func (c *orphanCursor) nextShard() {
	if c.objects != nil {
		_ = c.objects.Close()
		c.objects = nil
	}
	c.shard++
	if c.shard == 256 {
		c.project = ""
	}
}
