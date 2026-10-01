package sourcecatalog

import (
	"context"
	"errors"
	"strings"
	"time"
)

// TreeFilePageLimit bounds one page of file rows.
const TreeFilePageLimit = 512

// Audience comes from the host entry point.
type Audience uint8

const (
	AgentAudience Audience = iota
	HumanAudience
)

// FileScope separates agent metadata exclusion from human hidden-file filters.
// Capture and tool admission apply their own current source policies as well.
type FileScope struct {
	Audience      Audience
	IncludeHidden bool
}

func (s FileScope) clause() string {
	clause := "indexed=1 AND regular=1"
	if s.Audience != HumanAudience {
		clause += " AND agent_metadata=0"
	}
	if !s.IncludeHidden {
		clause += " AND hidden=0"
	}
	return clause
}

func (s FileScope) countColumn() string {
	if s.Audience == HumanAudience {
		if s.IncludeHidden {
			return "files"
		}
		return "visible_files"
	}
	if s.IncludeHidden {
		return "agent_files"
	}
	return "agent_visible_files"
}

// pathIndex names the partial index holding exactly this scope's files in path
// order. Left to choose, the planner picks admitted_nodes and reads every
// hidden row, which sorts ahead of visible paths, before the first match.
func (s FileScope) pathIndex() string {
	if s.IncludeHidden {
		return "all_files"
	}
	return "visible_files"
}

const indexFileColumns = "path,parent,name,depth,is_dir,symlink,size,mode,modified"

// FilePage visits regular files in path order within the reader's generation.
func (r *IndexReader) FilePage(ctx context.Context, scope FileScope, after string, limit int) ([]Entry, error) {
	if limit <= 0 || limit > TreeFilePageLimit {
		return nil, errors.New("catalog file page limit is outside its bounds")
	}
	// #nosec G202 -- Scope selects fixed SQL predicates and indexes; values are bound.
	rows, err := r.tx.QueryContext(ctx,
		"SELECT "+indexFileColumns+" FROM nodes INDEXED BY "+scope.pathIndex()+" WHERE "+scope.clause()+" AND path>? ORDER BY path LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	entries := make([]Entry, 0, limit)
	for rows.Next() {
		var e Entry
		var modified int64
		if err := rows.Scan(&e.Path, &e.Parent, &e.Name, &e.Depth, &e.IsDir, &e.IsSymlink, &e.Size, &e.Mode, &modified); err != nil {
			return nil, err
		}
		e.Modified = time.Unix(0, modified).UTC()
		e.RootID = r.store.root.ID
		entries = append(entries, e)
		r.RowsRead++
	}
	return entries, rows.Err()
}

// FilePathsPage omits the metadata that path ranking does not consume.
func (r *IndexReader) FilePathsPage(ctx context.Context, scope FileScope, after string, limit int) ([]string, error) {
	if limit <= 0 || limit > TreeFilePageLimit {
		return nil, errors.New("catalog file page limit is outside its bounds")
	}
	return r.filePaths(ctx, scope, scope.pathIndex(), "path>? ORDER BY path LIMIT ?", after, limit)
}

// LiteralFilePathsPage narrows path candidates with required substrings. The
// caller still verifies its full query, including flags and path predicates.
func (r *IndexReader) LiteralFilePathsPage(ctx context.Context, scope FileScope, literals []string, caseSensitive bool, after string, limit int) ([]string, error) {
	if limit <= 0 || limit > TreeFilePageLimit {
		return nil, errors.New("catalog file page limit is outside its bounds")
	}
	column := "path"
	if !caseSensitive {
		column = "search_path"
	}
	clause := "path>?"
	args := []any{after}
	for _, literal := range literals {
		if !caseSensitive {
			literal = strings.ToLower(literal)
		}
		clause += " AND instr(" + column + ",?)>0"
		args = append(args, literal)
	}
	args = append(args, limit)
	return r.filePaths(ctx, scope, scope.pathIndex(), clause+" ORDER BY path LIMIT ?", args...)
}

// FileAddressPage serves the empty file-picker query in its ranking order.
func (r *IndexReader) FileAddressPage(ctx context.Context, scope FileScope, afterLength int, afterPath string, limit int) ([]string, error) {
	if limit <= 0 || limit > TreeFilePageLimit {
		return nil, errors.New("catalog file page limit is outside its bounds")
	}
	return r.filePaths(ctx, scope, "",
		"(length(CAST(path AS BLOB)),path)>(?,?) ORDER BY length(CAST(path AS BLOB)),path LIMIT ?", afterLength, afterPath, limit)
}

type FilePathMatch int

const (
	FileBasenamePrefix FilePathMatch = iota
	FileBasenameContains
	FilePathSubsequence
)

// MatchingFilePathsPage filters one ranking tier before paths cross the
// database boundary.
func (r *IndexReader) MatchingFilePathsPage(ctx context.Context, scope FileScope, query string, match FilePathMatch, after string, limit int) ([]string, error) {
	if limit <= 0 || limit > TreeFilePageLimit {
		return nil, errors.New("catalog file page limit is outside its bounds")
	}
	query = strings.ToLower(query)
	switch match {
	case FileBasenamePrefix:
		return r.filePaths(ctx, scope, "file_name", "path>? AND search_name GLOB ? ORDER BY path LIMIT ?", after, pathGlobLiteral(query)+"*", limit)
	case FileBasenameContains:
		return r.filePaths(ctx, scope, scope.pathIndex(), "path>? AND instr(search_name,?)>1 ORDER BY path LIMIT ?", after, query, limit)
	case FilePathSubsequence:
		var pattern strings.Builder
		pattern.WriteByte('*')
		for _, char := range query {
			pattern.WriteString(pathGlobLiteral(string(char)))
			pattern.WriteByte('*')
		}
		return r.filePaths(ctx, scope, scope.pathIndex(), "path>? AND instr(search_name,?)=0 AND search_path GLOB ? ORDER BY path LIMIT ?", after, query, pattern.String(), limit)
	default:
		return nil, errors.New("unknown path match tier")
	}
}

func pathGlobLiteral(query string) string {
	var literal strings.Builder
	for _, char := range query {
		switch char {
		case '*', '?', '[':
			literal.WriteByte('[')
			literal.WriteRune(char)
			literal.WriteByte(']')
		default:
			literal.WriteRune(char)
		}
	}
	return literal.String()
}

// NamedFiles resolves a bounded set of case-insensitive repository resources.
func (r *IndexReader) NamedFiles(ctx context.Context, scope FileScope, names []string) ([]string, error) {
	if len(names) == 0 || len(names) > TreeFilePageLimit {
		return nil, errors.New("catalog named file query is outside its bounds")
	}
	args := make([]any, len(names))
	for i, name := range names {
		args[i] = strings.ToLower(name)
	}
	return r.filePaths(ctx, scope, "",
		"lower(path) IN ("+strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")+") ORDER BY path", args...)
}

func (r *IndexReader) filePaths(ctx context.Context, scope FileScope, index, clause string, args ...any) ([]string, error) {
	from := "nodes"
	if index != "" {
		from += " INDEXED BY " + index
	}
	// #nosec G202 -- index, scope and clause are internal SQL templates; query values are bound parameters.
	rows, err := r.tx.QueryContext(ctx, "SELECT path FROM "+from+" WHERE "+scope.clause()+" AND "+clause, args...)
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

// FileCount is the regular files the published generation holds, which is
// fewer than the tree holds when directories were refused.
func (r *IndexReader) FileCount(ctx context.Context, scope FileScope) (int, error) {
	var count int
	err := r.tx.QueryRowContext(ctx, "SELECT "+scope.countColumn()+" FROM totals WHERE id=1").Scan(&count)
	r.RowsRead++
	return count, err
}
