package sourcecatalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/textrank"
)

const treeColumns = "path,parent,name,depth,is_dir,symlink,vcs,size,mode,modified,files,bytes,children,representative,boundary,pruned"

// ErrTreeCursor means a continuation no longer identifies the requested page.
var ErrTreeCursor = errors.New("catalog continuation is invalid")

type treeScanner interface{ Scan(...any) error }

func scanTreeNode(row treeScanner) (TreeNode, error) {
	var n TreeNode
	var modified int64
	err := row.Scan(&n.Path, &n.Parent, &n.Name, &n.Depth, &n.IsDir, &n.IsSymlink, &n.IsVCSRoot, &n.Size, &n.Mode, &modified, &n.Files, &n.Bytes, &n.Children, &n.Representative, &n.Boundary, &n.Pruned)
	n.Modified = time.Unix(0, modified).UTC()
	return n, err
}

func readTreeNode(ctx context.Context, tx *sql.Tx, rel string) (TreeNode, error) {
	return scanTreeNode(tx.QueryRowContext(ctx, "SELECT "+treeColumns+" FROM nodes WHERE path=?", rel))
}

// Node reads one indexed path and its subtree aggregates.
func (r *SummaryReader) Node(ctx context.Context, rel string) (TreeNode, error) {
	r.RowsRead++
	return readTreeNode(ctx, r.tx, normalizeDir(rel))
}

// TreePage carries a bounded child selection and exact unreturned material.
type TreePage struct {
	Nodes             []TreeNode
	RemainingFiles    int
	RemainingBytes    int64
	RemainingChildren int
	Next              string
}

type treePosition struct {
	Promoted []string
	Plan     string
	Parent   string
	After    string
	Seen     int
	Files    int
	Bytes    int64
}

// Page uses indexed keyset pagination. Task and documentation candidates have
// a bounded reserved share; canonical pages omit those same promoted entries.
func (r *SummaryReader) Page(ctx context.Context, parent TreeNode, cursor, task string, preferred []string, limit int) (TreePage, error) {
	if limit <= 0 {
		return TreePage{}, errors.New("catalog page requires a positive limit")
	}
	position := treePosition{Parent: parent.Path}
	planRaw, err := json.Marshal(struct {
		Task  string
		Limit int
	}{task, limit})
	if err != nil {
		return TreePage{}, err
	}
	plan := sha256.Sum256(planRaw)
	position.Plan = base64.RawURLEncoding.EncodeToString(plan[:])
	expectedPlan := position.Plan
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return TreePage{}, fmt.Errorf("%w: %w", ErrTreeCursor, err)
		}
		if err = json.Unmarshal(raw, &position); err != nil {
			return TreePage{}, fmt.Errorf("%w: %w", ErrTreeCursor, err)
		}
		if position.Plan != expectedPlan || position.Parent != parent.Path || position.Seen < 0 || position.Seen > parent.Children {
			return TreePage{}, ErrTreeCursor
		}
	}
	promoted, err := r.pagePromotions(ctx, parent.Path, task, preferred, cursor == "", &position, min(4, max(1, limit/2)))
	if err != nil {
		return TreePage{}, err
	}
	out := TreePage{}
	if cursor == "" {
		out.Nodes = append(out.Nodes, promoted...)
	}
	query := "SELECT " + treeColumns + " FROM nodes WHERE parent=? AND boundary=0 AND rank>?"
	args := []any{parent.Path, position.After}
	for _, n := range promoted {
		query += " AND path<>?"
		args = append(args, n.Path)
	}
	query += " ORDER BY rank LIMIT ?"
	args = append(args, limit-len(out.Nodes)+1)
	rows, err := r.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return TreePage{}, err
	}
	defer func() { _ = rows.Close() }()
	more := false
	for rows.Next() {
		n, scanErr := scanTreeNode(rows)
		if scanErr != nil {
			return TreePage{}, scanErr
		}
		r.RowsRead++
		if len(out.Nodes) == limit {
			more = true
			break
		}
		out.Nodes = append(out.Nodes, n)
		position.After = treeRank(n)
	}
	if err := rows.Err(); err != nil {
		return TreePage{}, err
	}
	for _, n := range out.Nodes {
		position.Seen++
		position.Files += n.Files
		position.Bytes += n.Bytes
	}
	out.RemainingChildren = parent.Children - position.Seen
	out.RemainingFiles = parent.Files - position.Files
	out.RemainingBytes = parent.Bytes - position.Bytes
	if more {
		raw, marshalErr := json.Marshal(position)
		if marshalErr != nil {
			return TreePage{}, marshalErr
		}
		out.Next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, nil
}

func (r *SummaryReader) pagePromotions(ctx context.Context, parent, task string, preferred []string, first bool, position *treePosition, limit int) ([]TreeNode, error) {
	if first {
		nodes, err := r.promoted(ctx, parent, task, preferred, limit)
		for _, n := range nodes {
			position.Promoted = append(position.Promoted, n.Path)
		}
		return nodes, err
	}
	if len(position.Promoted) > limit {
		return nil, ErrTreeCursor
	}
	nodes := make([]TreeNode, 0, len(position.Promoted))
	for _, p := range position.Promoted {
		n, err := r.Node(ctx, p)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTreeCursor
		}
		if err != nil {
			return nil, err
		}
		if n.Parent != parent || n.Boundary {
			return nil, ErrTreeCursor
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func (r *SummaryReader) promoted(ctx context.Context, parent, task string, preferred []string, limit int) ([]TreeNode, error) {
	out := make([]TreeNode, 0, limit)
	seen := map[string]bool{}
	add := func(candidate string) error {
		if len(out) == limit || !treeUnder(candidate, parent) || candidate == parent {
			return nil
		}
		rel := candidate
		if parent != "." {
			rel = strings.TrimPrefix(candidate, parent+"/")
		}
		child := path.Join(parent, strings.SplitN(rel, "/", 2)[0])
		if seen[child] {
			return nil
		}
		seen[child] = true
		n, err := r.Node(ctx, child)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err == nil && !n.Boundary {
			out = append(out, n)
		}
		return err
	}
	for _, p := range preferred {
		if err := add(p); err != nil {
			return nil, err
		}
		if len(out) == limit {
			return out, nil
		}
	}
	terms := textrank.AnalyzeQuery(task, true, true)
	for _, term := range terms[:min(len(terms), 8)] {
		candidates, err := r.termPaths(ctx, parent, term, limit)
		if err != nil {
			return nil, err
		}
		for _, p := range candidates {
			if err := add(p); err != nil {
				return nil, err
			}
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (r *SummaryReader) termPaths(ctx context.Context, parent, term string, limit int) ([]string, error) {
	lo, hi := "", "\U0010ffff"
	if parent != "." {
		lo, hi = parent+"/", parent+"0"
	}
	rows, err := r.tx.QueryContext(ctx, "SELECT path FROM terms WHERE term=? AND path>=? AND path<? ORDER BY path LIMIT ?", term, lo, hi, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
		r.RowsRead++
	}
	return paths, rows.Err()
}

const summaryFilesPageQuery = "SELECT " + treeColumns + " FROM nodes INDEXED BY source_page WHERE is_dir=0 AND boundary=0 AND symlink=0 AND files=1 AND path>? AND path<? ORDER BY path LIMIT ?"

// FilesPage reads a bounded lexical page of regular source candidates in a scope.
// After is exclusive; callers bind it to the scope and published generation.
func (r *SummaryReader) FilesPage(ctx context.Context, base, after string, limit int) ([]TreeNode, bool, error) {
	if limit <= 0 {
		return nil, false, errors.New("catalog file page requires a positive limit")
	}
	lo, hi := "", "\U0010ffff"
	if base != "." {
		lo, hi = base+"/", base+"0"
	}
	rows, err := r.tx.QueryContext(ctx, summaryFilesPageQuery, max(lo, after), hi, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	var out []TreeNode
	for rows.Next() {
		n, err := scanTreeNode(rows)
		if err != nil {
			return nil, false, err
		}
		r.RowsRead++
		if len(out) == limit {
			return out, true, nil
		}
		out = append(out, n)
	}
	return out, false, rows.Err()
}
