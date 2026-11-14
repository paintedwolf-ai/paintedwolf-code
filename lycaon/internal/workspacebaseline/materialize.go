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
	overlay := map[string]overlayEntry{}
	if overlayPath != "" {
		reader, err := Open(ctx, overlayPath, blobs)
		if err != nil {
			return MaterializeReport{}, err
		}
		defer func() { _ = reader.Close() }()
		err = reader.EachIdentity(ctx, roots, func(entry Entry) error {
			overlay[identityKey(entry.RootID, entry.ScopeRel)] = overlayEntry{entry: entry}
			return nil
		})
		if err != nil {
			return MaterializeReport{}, err
		}
	}
	m := materializer{ctx: ctx, roots: roots, branch: branch, blobs: baseline.blobs}
	err = baseline.EachIdentity(ctx, roots, func(entry Entry) error {
		if _, changed := overlay[identityKey(entry.RootID, entry.ScopeRel)]; changed {
			return nil
		}
		if !entry.File.Opaque || entry.File.LinkTarget != "" {
			return m.write(entry)
		}
		src := ""
		if current != nil {
			src = current(entry.Path)
		}
		if src == "" {
			m.report.Unavailable = append(m.report.Unavailable, entry.Path)
			return nil
		}
		return m.standIn(entry, src)
	})
	if err != nil {
		return MaterializeReport{}, err
	}
	for _, item := range overlay {
		entry := item.entry
		if entry.File.Deleted {
			if err := m.remove(entry); err != nil {
				return MaterializeReport{}, err
			}
			continue
		}
		if entry.File.Opaque && entry.File.LinkTarget == "" {
			return MaterializeReport{}, fmt.Errorf("overlay body for %s was not captured", entry.Path)
		}
		if err := m.write(entry); err != nil {
			return MaterializeReport{}, err
		}
	}
	return m.report, nil
}

// identityKey keys overlay lookups by structured identity.
func identityKey(rootID, scopeRel string) string {
	return rootID + "\x00" + scopeRel
}

type overlayEntry struct {
	entry Entry
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

func (m *materializer) dest(entry Entry) (string, error) {
	abs, err := branchPathAt(m.roots, m.branch, entry.RootID, entry.ScopeRel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return "", err
	}
	return abs, nil
}

func (m *materializer) remove(entry Entry) error {
	abs, err := branchPathAt(m.roots, m.branch, entry.RootID, entry.ScopeRel)
	if err != nil {
		return err
	}
	err = os.RemoveAll(abs)
	if err == nil {
		m.report.Deleted++
	}
	return err
}

func (m *materializer) write(entry Entry) error {
	f := entry.File
	if err := m.ctx.Err(); err != nil {
		return err
	}
	abs, err := m.dest(entry)
	if err != nil {
		return err
	}
	_ = os.RemoveAll(abs)
	if f.LinkTarget != "" {
		if err := os.Symlink(f.LinkTarget, abs); err != nil {
			return fmt.Errorf("restore symlink %s: %w", entry.Path, err)
		}
		m.report.Files++
		return lchtimes(abs, time.Unix(0, f.MtimeNano))
	}
	if f.SHA256 == "" {
		return fmt.Errorf("baseline %s has no body to restore", entry.Path)
	}
	out, err := os.OpenFile(abs, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(f.Mode).Perm()) // #nosec G304 -- branch-confined path from branchPathAt
	if err != nil {
		return err
	}
	if err := m.blobs.CopySHA(m.ctx, f.SHA256, out); err != nil {
		_ = out.Close()
		_ = os.Remove(abs)
		return fmt.Errorf("restore %s: %w", entry.Path, err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	return m.finish(abs, f)
}

// standIn clones the caller's current file for an opaque baseline body.
func (m *materializer) standIn(entry Entry, src string) error {
	f := entry.File
	if err := m.ctx.Err(); err != nil {
		return err
	}
	abs, err := m.dest(entry)
	if err != nil {
		return err
	}
	_ = os.RemoveAll(abs)
	cloned, err := fileclone.Clone(src, abs)
	if err != nil {
		return fmt.Errorf("restore %s: %w", entry.Path, err)
	}
	if !cloned {
		if err := copyFile(src, abs, os.FileMode(f.Mode).Perm()); err != nil {
			return fmt.Errorf("restore %s: %w", entry.Path, err)
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
