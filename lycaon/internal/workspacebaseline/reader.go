// Package workspacebaseline stores immutable worker branch manifests and lazy merge inputs.
package workspacebaseline

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"

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

type Reader struct {
	db    *sql.DB
	blobs *sourceblob.Store
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
	if format != 1 {
		_ = d.Close()
		return nil, fmt.Errorf("unsupported workspace manifest format %d", format)
	}
	err = d.QueryRowContext(ctx, "SELECT path,"+fileColumns+" FROM files LIMIT 0").Scan()
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		_ = d.Close()
		return nil, fmt.Errorf("invalid workspace manifest shape: %w", err)
	}
	return &Reader{db: d, blobs: blobs}, nil
}

func (r *Reader) Close() error { return r.db.Close() }

const fileColumns = "size, mtime_ns, sha256, opaque, mode, link_target, deleted"

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

// Each visits every manifest row in path order, tombstones included.
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

type boundedBody struct{ bytes.Buffer }

func (b *boundedBody) Write(p []byte) (int, error) {
	if b.Len()+len(p) > MaxContentBytes {
		return 0, fmt.Errorf("workspace baseline content exceeds limit")
	}
	return b.Buffer.Write(p)
}
