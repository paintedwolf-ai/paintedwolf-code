package workspacebaseline

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceblob"
)

// MaterializeReport describes one rebuilt branch tree.
type MaterializeReport struct {
	Files   int
	Bytes   int64
	Deleted int
	// Unavailable lists opaque baseline paths with no stand-in on disk; the
	// rebuilt tree lacks them.
	Unavailable []string
}

// Materialize rebuilds a branch tree from its baseline and overlay manifests,
// restoring modes and modification times so the baseline comparison reports
// exactly the overlay's changes. Opaque baseline bodies are not pinned; current
// names the file that stands in for one, or "" when nothing can.
func Materialize(ctx context.Context, baselinePath, overlayPath string, blobs *sourceblob.Store, roots []projectroot.RootRef, branch string, current func(path string) string) (MaterializeReport, error) {
	baseline, err := Open(ctx, baselinePath, blobs)
	if err != nil {
		return MaterializeReport{}, err
	}
	defer func() { _ = baseline.Close() }()
	overlay := map[string]File{}
	if overlayPath != "" {
		reader, err := Open(ctx, overlayPath, blobs)
		if err != nil {
			return MaterializeReport{}, err
		}
		defer func() { _ = reader.Close() }()
		err = reader.Each(ctx, func(path string, f File) error {
			overlay[path] = f
			return nil
		})
		if err != nil {
			return MaterializeReport{}, err
		}
	}
	m := materializer{ctx: ctx, roots: roots, branch: branch, blobs: baseline.blobs}
	err = baseline.Each(ctx, func(path string, f File) error {
		if _, changed := overlay[path]; changed {
			return nil
		}
		if !f.Opaque || f.LinkTarget != "" {
			return m.write(path, f)
		}
		src := ""
		if current != nil {
			src = current(path)
		}
		if src == "" {
			m.report.Unavailable = append(m.report.Unavailable, path)
			return nil
		}
		return m.standIn(path, f, src)
	})
	if err != nil {
		return MaterializeReport{}, err
	}
	for path, f := range overlay {
		if f.Deleted {
			if err := m.remove(path); err != nil {
				return MaterializeReport{}, err
			}
			continue
		}
		if f.Opaque && f.LinkTarget == "" {
			return MaterializeReport{}, fmt.Errorf("overlay body for %s was not captured", path)
		}
		if err := m.write(path, f); err != nil {
			return MaterializeReport{}, err
		}
	}
	return m.report, nil
}

type materializer struct {
	ctx    context.Context
	roots  []projectroot.RootRef
	branch string
	blobs  interface {
		CopySHA(ctx context.Context, sha string, dst io.Writer) error
	}
	report MaterializeReport
}

func (m *materializer) dest(path string) (string, error) {
	abs, err := branchPath(m.roots, m.branch, path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return "", err
	}
	return abs, nil
}

func (m *materializer) remove(path string) error {
	abs, err := branchPath(m.roots, m.branch, path)
	if err != nil {
		return err
	}
	err = os.RemoveAll(abs)
	if err == nil {
		m.report.Deleted++
	}
	return err
}

func (m *materializer) write(path string, f File) error {
	if err := m.ctx.Err(); err != nil {
		return err
	}
	abs, err := m.dest(path)
	if err != nil {
		return err
	}
	_ = os.RemoveAll(abs)
	if f.LinkTarget != "" {
		if err := os.Symlink(f.LinkTarget, abs); err != nil {
			return fmt.Errorf("restore symlink %s: %w", path, err)
		}
		m.report.Files++
		return lchtimes(abs, time.Unix(0, f.MtimeNano))
	}
	if f.SHA256 == "" {
		return fmt.Errorf("baseline %s has no body to restore", path)
	}
	out, err := os.OpenFile(abs, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(f.Mode).Perm()) // #nosec G304 -- branch-confined path from branchPath
	if err != nil {
		return err
	}
	if err := m.blobs.CopySHA(m.ctx, f.SHA256, out); err != nil {
		_ = out.Close()
		_ = os.Remove(abs)
		return fmt.Errorf("restore %s: %w", path, err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	return m.finish(abs, f)
}

// standIn clones the caller's current file for an opaque baseline body.
func (m *materializer) standIn(path string, f File, src string) error {
	if err := m.ctx.Err(); err != nil {
		return err
	}
	abs, err := m.dest(path)
	if err != nil {
		return err
	}
	_ = os.RemoveAll(abs)
	cloned, err := fileclone.Clone(src, abs)
	if err != nil {
		return fmt.Errorf("restore %s: %w", path, err)
	}
	if !cloned {
		if err := copyFile(src, abs, os.FileMode(f.Mode).Perm()); err != nil {
			return fmt.Errorf("restore %s: %w", path, err)
		}
	}
	return m.finish(abs, f)
}

func (m *materializer) finish(abs string, f File) error {
	if err := os.Chmod(abs, os.FileMode(f.Mode).Perm()); err != nil {
		return err
	}
	stamp := time.Unix(0, f.MtimeNano)
	if err := os.Chtimes(abs, stamp, stamp); err != nil {
		return err
	}
	m.report.Files++
	m.report.Bytes += f.Size
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src) // #nosec G304 -- caller-owned source path
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode) // #nosec G304 -- branch-confined destination
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
