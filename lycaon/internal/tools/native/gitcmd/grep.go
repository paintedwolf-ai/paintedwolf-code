package gitcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// grepOperands requires a pattern and at most one matcher.
func grepOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	matchers := 0
	for _, key := range []string{"-E", "-F", "-P", "-G"} {
		if has(bound, key) {
			matchers++
		}
	}
	if matchers > 1 {
		return operandInvalid(spec, spec.Name, "at most one of -E, -F, -P or -G")
	}
	if len(bound.Options("-e")) == 0 && len(bound.Operands()) == 0 {
		return operandInvalid(spec, spec.Name, "a PATTERN")
	}
	return nil
}

// grepSource is one searched object set: a revision or tree, the index, or
// the tracked working tree.
type grepSource struct {
	label  string
	tree   string
	prefix bool
}

type grepFile struct {
	path, oid string
}

type gitGrep struct {
	ctx      context.Context
	p        Prepared
	root     string
	prefix   string
	matcher  *textsearch.Matcher
	out      *meteredOutput
	stderr   io.Writer
	limit    int64
	matched  bool
	nul      byte
	numbers  bool
	names    bool
	mode     string
	invert   bool
	pathspec []string
}

// grep searches committed, staged or working-tree content with the shared
// native regex engines and prints Git's REV:PATH:LINE layout.
func (p Prepared) grep(ctx context.Context, streams commandprogram.Streams) (int, error) {
	stderr := streams.Error
	if stderr == nil {
		stderr = io.Discard
	}
	dir, repo, status, err := resolveCommandRepo(ctx, p, stderr, false)
	if err != nil || status != 0 {
		return status, err
	}
	out := &meteredOutput{ctx: ctx, w: streams.Output, maximum: p.owners.limit("stream_bytes")}
	if out.w == nil {
		out.w = io.Discard
	}
	g := &gitGrep{ctx: ctx, p: p, root: repo.Root, out: out, stderr: stderr, limit: p.owners.limit("stream_bytes"), nul: ':',
		numbers: has(p.Argv, "-n"), names: !has(p.Argv, "-h"), invert: has(p.Argv, "-v")}
	if has(p.Argv, "-z") {
		g.nul = 0
	}
	switch {
	case has(p.Argv, "-q"):
		g.mode = "quiet"
	case has(p.Argv, "-l"):
		g.mode = "names"
	case has(p.Argv, "-c"):
		g.mode = "count"
	}
	if rel, relErr := filepath.Rel(repo.Root, dir); relErr == nil && rel != "." {
		g.prefix = filepath.ToSlash(rel)
	}
	sources, patterns, status, err := g.operands(dir)
	if err != nil || status != 0 {
		return status, err
	}
	spec := textsearch.Spec{Patterns: patterns, Dialect: textsearch.Basic, IgnoreCase: has(p.Argv, "-i"), Word: has(p.Argv, "-w")}
	for key, dialect := range map[string]textsearch.Dialect{"-E": textsearch.Extended, "-F": textsearch.Fixed, "-P": textsearch.Perl} {
		if has(p.Argv, key) {
			spec.Dialect = dialect
		}
	}
	limits, err := textsearch.CatalogLimits()
	if err != nil {
		return 0, err
	}
	g.matcher, err = textsearch.Compile(ctx, spec, limits)
	var invalid *textsearch.PatternError
	if errors.As(err, &invalid) {
		writeLine(stderr, fmt.Sprintf("fatal: command line, '%s': %s", invalid.Pattern, invalid.Detail))
		return 128, nil
	}
	if err != nil {
		return 0, err
	}
	for _, source := range sources {
		if err := g.search(source); err != nil {
			if out.err != nil {
				return 0, out.err
			}
			return 0, grepBudget(err)
		}
		if g.mode == "quiet" && g.matched {
			break
		}
	}
	if g.matched {
		return 0, nil
	}
	return 1, nil
}

// operands splits patterns, revisions and paths. Before --, an operand that
// names a tree is a revision until the first one that is not; the rest must
// be paths in the working tree, as Git requires.
func (g *gitGrep) operands(dir string) ([]grepSource, []string, int, error) {
	var patterns []string
	for _, option := range g.p.Argv.Options("-e") {
		for _, value := range option.Values {
			patterns = append(patterns, value.Value)
		}
	}
	before, after := g.p.command.beforeDash, g.p.command.afterDash
	switch {
	case len(patterns) > 0:
	case len(before) > 0:
		patterns, before = []string{before[0]}, before[1:]
	default:
		// Without -e, the first word after -- is still the pattern; the
		// operand rule guarantees one exists.
		patterns, after = []string{after[0]}, after[1:]
	}
	var sources []grepSource
	var paths []string
	for _, word := range before {
		if len(paths) == 0 {
			tree, err := resolveTree(g.ctx, g.root, word)
			if err != nil {
				return nil, nil, 0, err
			}
			if tree != "" {
				sources = append(sources, grepSource{label: word + ":", tree: tree, prefix: !strings.Contains(word, ":")})
				continue
			}
		}
		target := word
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		if _, err := os.Lstat(target); err != nil {
			writeLine(g.stderr, fmt.Sprintf("fatal: ambiguous argument '%s': unknown revision or path not in the working tree.\nUse '--' to separate paths from revisions, like this:\n'git <command> [<revision>...] -- [<file>...]'", word))
			return nil, nil, 128, nil
		}
		paths = append(paths, word)
	}
	paths = append(paths, after...)
	rels, err := repoRelPaths(g.ctx, g.p, dir, g.root, paths)
	if err != nil {
		return nil, nil, 0, err
	}
	g.pathspec = rels
	if len(g.pathspec) == 0 && g.prefix != "" {
		g.pathspec = []string{g.prefix}
	}
	if len(sources) == 0 {
		label := "worktree"
		if has(g.p.Argv, "--cached") {
			label = "index"
		}
		sources = []grepSource{{tree: label, prefix: true}}
	}
	return sources, patterns, 0, nil
}

func (g *gitGrep) search(source grepSource) error {
	files, err := g.files(source)
	if err != nil {
		return err
	}
	if source.tree == "worktree" {
		for _, file := range files {
			content, ok, err := g.readWorktree(file.path)
			if err != nil {
				return err
			}
			if ok {
				if err := g.match(source, file.path, content); err != nil {
					return err
				}
			}
			if g.mode == "quiet" && g.matched {
				return nil
			}
		}
		return nil
	}
	oids := make([]string, len(files))
	for i, file := range files {
		oids[i] = file.oid
	}
	return git.ReadBlobs(g.ctx, g.root, oids, g.limit, func(i int, content []byte, err error) error {
		if errors.Is(err, git.ErrBlobTooLarge) {
			observed, sizeErr := g.p.owners.Git.BlobSize(g.ctx, g.root, files[i].oid)
			if sizeErr != nil {
				return sizeErr
			}
			return &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": "stream_bytes", "resource_unit": "bytes", "maximum": g.limit, "observed": observed, "path": files[i].path}}
		}
		if g.mode == "quiet" && g.matched {
			return nil
		}
		return g.match(source, files[i].path, content)
	})
}

// files lists blobs and symbolic links in Git's path order.
func (g *gitGrep) files(source grepSource) ([]grepFile, error) {
	args := []string{"--literal-pathspecs", "ls-tree", "-r", "-z", "--full-tree", source.tree, "--"}
	if source.tree == "worktree" || source.tree == "index" {
		args = []string{"--literal-pathspecs", "ls-files", "-s", "-z", "--"}
	}
	if source.prefix {
		args = append(args, g.pathspec...)
	}
	var files []grepFile
	err := gitexec.RunRecords(g.ctx, g.root, args, gitexec.Opts{Profile: gitexec.ProfileHermetic}, func(raw []byte) error {
		meta, name, ok := strings.Cut(string(raw), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return errors.New("malformed Git tree entry")
		}
		mode, oid := fields[0], fields[1]
		if source.tree == "worktree" || source.tree == "index" {
			if fields[2] != "0" {
				return nil
			}
		} else if fields[1] != "blob" {
			return nil
		} else {
			oid = fields[2]
		}
		if mode == "160000" {
			return nil
		}
		files = append(files, grepFile{path: name, oid: oid})
		return nil
	})
	return files, err
}

func (g *gitGrep) readWorktree(rel string) ([]byte, bool, error) {
	abs := entryAbs(g.root, rel)
	info, err := os.Lstat(abs)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(abs)
		return []byte(target), err == nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, nil
	}
	// The descriptor-relative open refuses a parent replaced by a link.
	f, err := fseffect.OpenRead(fseffect.Location{Root: g.root, Rel: filepath.FromSlash(rel)})
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	content, err := io.ReadAll(io.LimitReader(f, g.limit+1))
	if err == nil && int64(len(content)) > g.limit {
		return nil, false, &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": "stream_bytes", "resource_unit": "bytes", "maximum": g.limit, "observed": int64(len(content)), "path": rel}}
	}
	return content, err == nil, err
}

// match prints one file's selected lines. Binary content, recognised by a NUL
// in its first 8000 bytes, reports one line instead of its matches.
func (g *gitGrep) match(source grepSource, rel string, content []byte) error {
	name := source.label + g.display(source, rel)
	binary := bytes.IndexByte(content[:min(len(content), 8000)], 0) >= 0
	count := 0
	_, err := textsearch.ScanReader(g.ctx, bytes.NewReader(content), int64(len(content)), nil, g.matcher, textsearch.ScanOptions{Invert: g.invert, MaxSelected: -1}, func(line textsearch.Line) error {
		if line.Kind != textsearch.SelectedLine {
			return nil
		}
		count++
		if g.mode != "" || binary {
			return nil
		}
		var record bytes.Buffer
		if g.names {
			record.WriteString(name)
			record.WriteByte(g.nul)
		}
		if g.numbers {
			record.WriteString(strconv.Itoa(line.Number))
			record.WriteByte(g.nul)
		}
		record.Write(line.Text)
		record.WriteByte('\n')
		_, err := g.out.Write(record.Bytes())
		return err
	})
	if err != nil || count == 0 {
		return err
	}
	g.matched = true
	switch {
	case g.mode == "quiet":
		return nil
	case g.mode == "names":
		end := byte('\n')
		if g.nul == 0 {
			end = 0
		}
		_, err = fmt.Fprintf(g.out, "%s%c", name, end)
	case g.mode == "count":
		_, err = fmt.Fprintf(g.out, "%s%c%d\n", name, g.nul, count)
	case binary:
		_, err = fmt.Fprintf(g.out, "Binary file %s matches\n", name)
	}
	return err
}

// display shows commit paths relative to the command directory; a tree
// named with REV:PATH keeps paths relative to that tree.
func (g *gitGrep) display(source grepSource, rel string) string {
	if !source.prefix || g.prefix == "" {
		return rel
	}
	up := ""
	base := g.prefix
	for base != "." && !strings.HasPrefix(rel, base+"/") {
		base = path.Dir(base)
		up += "../"
	}
	if base == "." {
		return up + rel
	}
	return up + strings.TrimPrefix(rel, base+"/")
}

// resolveTree peels a revision to its tree; REV:PATH already names one.
func resolveTree(ctx context.Context, root, word string) (string, error) {
	if !strings.Contains(word, ":") {
		return git.ResolveOptionalObject(ctx, root, word+"^{tree}")
	}
	oid, err := git.ResolveOptionalObject(ctx, root, word)
	if err != nil || oid == "" {
		return "", err
	}
	return git.ResolveOptionalObject(ctx, root, oid+"^{tree}")
}

func grepBudget(err error) error {
	var budget *textsearch.BudgetError
	if errors.As(err, &budget) {
		return errors.Join(err, &toolrejection.ToolReject{Code: textsearch.CodeMatchBudget, Data: map[string]any{"command": "git grep", "limit_id": budget.Resource, "maximum": budget.Maximum, "observed": budget.Observed}})
	}
	return err
}
