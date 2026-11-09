package workspacebaseline

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/textfile"
	_ "modernc.org/sqlite"
)

// MaxContentBytes bounds the bodies a baseline pins for text merging. Overlay
// captures pin every changed body regardless of size.
const MaxContentBytes = 2 << 20

// File records metadata even when the body cannot participate in a text merge.
//
// A baseline manifest lists the prepared branch before the worker ran. An
// overlay manifest lists only the paths the worker changed; there Deleted marks
// a tombstone and every regular file carries its body.
type File struct {
	Size      int64
	MtimeNano int64
	SHA256    string
	// Opaque bodies are not pinned: symlinks, and baseline files over MaxContentBytes.
	Opaque bool
	// Mode is the permission bits and file type as os.FileMode.
	Mode uint32
	// LinkTarget is the symlink destination when the entry is a symlink.
	LinkTarget string
	// Deleted marks a path the worker removed. Overlay manifests only.
	Deleted bool
}

// Entry is one manifest row with its structured identity. RootID and ScopeRel
// key the entry; Path is the display spelling kept for legacy readers.
type Entry struct {
	RootID   string
	ScopeRel string
	Path     string
	File     File
}

type Reader struct {
	db     *sql.DB
	blobs  *sourceblob.Store
	format int
}

func Open(ctx context.Context, path string, blobs *sourceblob.Store) (*Reader, error) {
	if path == "" || !filepath.IsAbs(path) || blobs == nil {
		return nil, fmt.Errorf("workspace baseline reference is missing or invalid")
	}
	u := url.URL{Scheme: "file", OmitHost: true, Path: filepath.ToSlash(path)}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("immutable", "1")
	u.RawQuery = q.Encode()
	d, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	var format int
	err = d.QueryRowContext(ctx, "SELECT version FROM manifest").Scan(&format)
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("open workspace baseline: %w", err)
	}
	// Format 1 manifests key entries by display path; format 2 keys them by
	// (root_id, scope_rel). Both stay readable for in-flight references.
	if format != 1 && format != 2 {
		_ = d.Close()
		return nil, fmt.Errorf("unsupported workspace manifest format %d", format)
	}
	columns := legacyKeyColumns
	if format == 2 {
		columns = identityKeyColumns
	}
	err = d.QueryRowContext(ctx, "SELECT "+columns+","+fileColumns+" FROM files LIMIT 0").Scan()
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		_ = d.Close()
		return nil, fmt.Errorf("invalid workspace manifest shape: %w", err)
	}
	return &Reader{db: d, blobs: blobs, format: format}, nil
}

func (r *Reader) Close() error { return r.db.Close() }

const fileColumns = "size, mtime_ns, sha256, opaque, mode, link_target, deleted"

const legacyKeyColumns = "path"
const identityKeyColumns = "root_id, scope_rel, path"

func scanFile(scan func(dest ...any) error, f *File) error {
	return scan(&f.Size, &f.MtimeNano, &f.SHA256, &f.Opaque, &f.Mode, &f.LinkTarget, &f.Deleted)
}

func (r *Reader) Lookup(ctx context.Context, path string) (File, bool, error) {
	var f File
	row := r.db.QueryRowContext(ctx, "SELECT "+fileColumns+" FROM files WHERE path = ?", path)
	err := scanFile(row.Scan, &f)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, false, nil
	}
	return f, err == nil, err
}

// LookupIdentity fetches one entry by its structured identity. Format 1
// manifests only recorded display spellings, so the identity is spelled back
// out the way capture wrote it.
func (r *Reader) LookupIdentity(ctx context.Context, roots []projectroot.RootRef, rootID, scopeRel string) (File, bool, error) {
	if r.format == 2 {
		var f File
		row := r.db.QueryRowContext(ctx, "SELECT "+fileColumns+" FROM files WHERE root_id = ? AND scope_rel = ?", rootID, scopeRel)
		err := scanFile(row.Scan, &f)
		if errors.Is(err, sql.ErrNoRows) {
			return File{}, false, nil
		}
		return f, err == nil, err
	}
	display, err := displayPathFor(roots, rootID, scopeRel)
	if err != nil {
		return File{}, false, err
	}
	return r.Lookup(ctx, display)
}

// Each visits every manifest row in display-path order, tombstones included.
func (r *Reader) Each(ctx context.Context, visit func(string, File) error) error {
	rows, err := r.db.QueryContext(ctx, "SELECT path, "+fileColumns+" FROM files ORDER BY path")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var path string
		var f File
		if err := rows.Scan(&path, &f.Size, &f.MtimeNano, &f.SHA256, &f.Opaque, &f.Mode, &f.LinkTarget, &f.Deleted); err != nil {
			return err
		}
		if err := visit(path, f); err != nil {
			return err
		}
	}
	return rows.Err()
}

// EachIdentity visits every manifest row with its structured identity, in
// identity order for format 2 and display order for format 1.
func (r *Reader) EachIdentity(ctx context.Context, roots []projectroot.RootRef, visit func(Entry) error) error {
	if r.format == 2 {
		rows, err := r.db.QueryContext(ctx, "SELECT "+identityKeyColumns+","+fileColumns+" FROM files ORDER BY root_id, scope_rel")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var entry Entry
			if err := rows.Scan(&entry.RootID, &entry.ScopeRel, &entry.Path, &entry.File.Size, &entry.File.MtimeNano, &entry.File.SHA256, &entry.File.Opaque, &entry.File.Mode, &entry.File.LinkTarget, &entry.File.Deleted); err != nil {
				return err
			}
			if err := visit(entry); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	return r.Each(ctx, func(path string, f File) error {
		rootID, scopeRel, err := parseDisplayPath(roots, path)
		if err != nil {
			return err
		}
		return visit(Entry{RootID: rootID, ScopeRel: scopeRel, Path: path, File: f})
	})
}

// Content distinguishes an absent file from an unreadable or opaque baseline.
func (r *Reader) Content(ctx context.Context, path string) (string, bool, error) {
	f, exists, err := r.Lookup(ctx, path)
	if err != nil || !exists {
		return "", exists, err
	}
	if f.Deleted {
		return "", false, nil
	}
	if f.Opaque || f.SHA256 == "" {
		return "", true, fmt.Errorf("workspace baseline is not mergeable text: %s", path)
	}
	var body boundedBody
	if err := r.blobs.CopySHA(ctx, f.SHA256, &body); err != nil {
		return "", true, fmt.Errorf("read workspace baseline %s: %w", path, err)
	}
	doc, _, err := textfile.Open(body.Bytes(), textfile.LimitsForRaw(MaxContentBytes))
	if err != nil {
		return "", true, err
	}
	return doc.Text(), true, nil
}

// parseDisplayPath splits a legacy display spelling into the root holding it
// and the path relative to that root. Bare paths are primary-relative; other
// roots are qualified with @label. Unlabeled roots spelled bare are ambiguous
// by construction; format 1 recorded them under the primary.
func parseDisplayPath(roots []projectroot.RootRef, path string) (rootID, scopeRel string, err error) {
	if len(roots) > 1 {
		primary, err := projectroot.PrimaryRoot(roots)
		if err != nil {
			return "", "", err
		}
		if strings.HasPrefix(path, "@") {
			label, rel, ok := strings.Cut(path[1:], "/")
			if !ok {
				return "", "", fmt.Errorf("invalid qualified baseline path")
			}
			for _, root := range roots {
				if strings.EqualFold(root.Label, label) {
					return root.ID, rel, nil
				}
			}
			return "", "", fmt.Errorf("unknown baseline root")
		}
		return primary.ID, path, nil
	}
	if len(roots) == 1 {
		return roots[0].ID, path, nil
	}
	return "", path, nil
}

// displayPathFor spells an identity the way format 1 capture wrote it.
func displayPathFor(roots []projectroot.RootRef, rootID, scopeRel string) (string, error) {
	if len(roots) <= 1 {
		return scopeRel, nil
	}
	primary, err := projectroot.PrimaryRoot(roots)
	if err != nil {
		return "", err
	}
	if rootID == primary.ID {
		return scopeRel, nil
	}
	for _, root := range roots {
		if root.ID == rootID {
			if root.Label == "" {
				return scopeRel, nil
			}
			return "@" + root.Label + "/" + scopeRel, nil
		}
	}
	return "", fmt.Errorf("unknown baseline root")
}

type boundedBody struct{ bytes.Buffer }

func (b *boundedBody) Write(p []byte) (int, error) {
	if b.Len()+len(p) > MaxContentBytes {
		return 0, fmt.Errorf("workspace baseline content exceeds limit")
	}
	return b.Buffer.Write(p)
}
