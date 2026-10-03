package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitlease"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// RestoreFile describes one exact file Git will restore, including index-only changes.
type RestoreFile struct {
	Path          string
	IndexOnly     bool
	IndexStage    int
	Before, After RestoreContent
}

type RestoreContent struct {
	Bytes  []byte
	SHA256 string
	Size   int64
	// Mode is a Git file mode; see gitFileMode.
	Mode   os.FileMode
	Exists bool
}

// gitFileMode reduces a mode to what Git records: a symlink, a submodule, or a
// regular file that is or is not executable. Other permission bits follow the
// umask, so a file Git rewrites can differ in them without changing.
func gitFileMode(mode os.FileMode) os.FileMode {
	switch {
	case mode&os.ModeSymlink != 0:
		return os.ModeSymlink | 0o777
	case mode&os.ModeDir != 0:
		return os.ModeDir | 0o755
	case mode&0o111 != 0:
		return 0o755
	default:
		return 0o644
	}
}

type restoreReview struct {
	source, indexTree, scratch string
	files                      []RestoreFile
	inputs                     map[string]RestoreContent
	previewBytes               int64
}

func (r *restoreReview) close() { _ = os.RemoveAll(r.scratch) }

func restoreGit(ctx context.Context, dir string, opts gitexec.Opts, args ...string) ([]byte, error) {
	out, code, err := gitexec.Run(ctx, dir, args, opts)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("prepare Git restore: %s", strings.TrimSpace(string(out)))
	}
	return out, nil
}

func prepareRestoreReview(ctx context.Context, dir, source string, staged, worktree bool, paths []string) (*restoreReview, error) {
	release, err := gitlease.Repository(ctx, dir)
	if err != nil {
		return nil, err
	}
	defer release()
	indexTree, err := restoreGit(ctx, dir, hermeticOpts(0), "write-tree")
	if err != nil {
		return nil, err
	}
	if source == "" {
		source = strings.TrimSpace(string(indexTree))
	}
	frozen, err := restoreGit(ctx, dir, hermeticOpts(0), "rev-parse", "--verify", "--end-of-options", source+"^{tree}")
	if err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp("", "restore-review-*")
	if err != nil {
		return nil, err
	}
	r := &restoreReview{source: strings.TrimSpace(string(frozen)), indexTree: strings.TrimSpace(string(indexTree)), scratch: scratch}
	r.inputs = make(map[string]RestoreContent)
	for _, flag := range []string{"--git-common-dir", "--git-dir"} {
		raw, err := restoreGit(ctx, dir, hermeticOpts(0), "rev-parse", "--path-format=absolute", flag)
		if err != nil {
			r.close()
			return nil, err
		}
		gitDir := strings.TrimSpace(string(raw))
		for _, suffix := range []string{"config", "config.worktree", "info/attributes"} {
			if err := r.captureInput(filepath.Join(gitDir, suffix)); err != nil {
				r.close()
				return nil, err
			}
		}
	}
	if err := r.prepareFiles(ctx, dir, staged, worktree, paths); err != nil {
		r.close()
		return nil, err
	}
	if err := r.validate(ctx, dir); err != nil {
		r.close()
		return nil, err
	}
	return r, nil
}

func (r *restoreReview) prepareFiles(ctx context.Context, dir string, staged, worktree bool, paths []string) error {
	top, ok := probeToplevel(ctx, dir)
	if !ok {
		return fmt.Errorf("restore repository root is unavailable")
	}
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	prefix, err := filepath.Rel(top, canonicalDir)
	if err != nil {
		return err
	}
	selected := make([]string, 0, len(paths))
	for _, path := range paths {
		selected = append(selected, filepath.ToSlash(filepath.Join(prefix, path)))
	}
	_, beforePaths, err := r.prepareTree(ctx, top, "before", r.indexTree, selected, false)
	if err != nil {
		return err
	}
	afterDir, afterPaths, err := r.prepareTree(ctx, top, "after", r.source, selected, worktree)
	if err != nil {
		return err
	}
	all := make(map[string]bool)
	for _, path := range append(beforePaths, afterPaths...) {
		all[path] = true
	}
	ordered := make([]string, 0, len(all))
	for path := range all {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	for _, path := range ordered {
		rel, err := filepath.Rel(prefix, filepath.FromSlash(path))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("restore target is outside the project")
		}
		if worktree {
			before, err := readRestoreContent(filepath.Join(top, path))
			if err != nil {
				return err
			}
			after, err := readRestoreContent(filepath.Join(afterDir, path))
			if err != nil {
				return err
			}
			r.appendFile(RestoreFile{Path: filepath.ToSlash(rel), Before: before, After: after})
		}
		if staged {
			before, err := readRestoreTreeFile(ctx, top, r.indexTree, path)
			if err != nil {
				return err
			}
			after, err := readRestoreTreeFile(ctx, top, r.source, path)
			if err != nil {
				return err
			}
			r.appendFile(RestoreFile{Path: filepath.ToSlash(rel), IndexOnly: true, Before: before, After: after})
		}
	}
	return nil
}

func (r *restoreReview) appendFile(file RestoreFile) {
	size := int64(len(file.Before.Bytes) + len(file.After.Bytes))
	if size > sourceledger.MaxRevisionContentBytes-r.previewBytes {
		file.Before.Bytes, file.After.Bytes = nil, nil
	} else {
		r.previewBytes += size
	}
	r.files = append(r.files, file)
}

func (r *restoreReview) prepareTree(ctx context.Context, top, label, tree string, paths []string, checkout bool) (string, []string, error) {
	opts := hermeticOpts(0)
	opts.IndexFile = filepath.Join(r.scratch, label+".index")
	if _, err := restoreGit(ctx, top, opts, "read-tree", tree); err != nil {
		return "", nil, err
	}
	args := append([]string{"--literal-pathspecs", "ls-files", "-z", "--"}, paths...)
	var selected []string
	if err := gitexec.RunRecords(ctx, top, args, opts, func(path []byte) error {
		selected = append(selected, string(path))
		return nil
	}); err != nil {
		return "", nil, err
	}
	if !checkout {
		return "", selected, nil
	}
	for _, path := range selected {
		for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
			if err := r.captureInput(filepath.Join(top, parent, ".gitattributes")); err != nil {
				return "", nil, err
			}
			if parent == "." {
				break
			}
		}
	}
	output := filepath.Join(r.scratch, label)
	if err := os.Mkdir(output, 0o700); err != nil {
		return "", nil, err
	}
	// Each path is already an exact index entry; bounded batches avoid argv limits.
	for start := 0; start < len(selected); start += 100 {
		end := min(start+100, len(selected))
		args = append([]string{"checkout-index", "--force", "--prefix=" + output + string(filepath.Separator), "--"}, selected[start:end]...)
		if _, err := restoreGit(ctx, top, opts, args...); err != nil {
			return "", nil, err
		}
	}
	return output, selected, nil
}

func readRestoreContent(path string) (RestoreContent, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return RestoreContent{}, nil
	}
	if err != nil {
		return RestoreContent{}, err
	}
	content := RestoreContent{Exists: true, Mode: gitFileMode(info.Mode())}
	var reader io.Reader
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		link, err := os.Readlink(path)
		if err != nil {
			return RestoreContent{}, err
		}
		reader = strings.NewReader(link)
	case info.Mode().IsRegular():
		file, err := os.Open(path)
		if err != nil {
			return RestoreContent{}, err
		}
		defer func() { _ = file.Close() }()
		reader = file
	default:
		return RestoreContent{}, fmt.Errorf("restore target is not a regular file or symlink: %s", path)
	}
	return readRestoreStream(content, reader)
}

func readRestoreStream(content RestoreContent, reader io.Reader) (RestoreContent, error) {
	hash := sha256.New()
	limited := io.LimitReader(io.TeeReader(reader, hash), sourceledger.MaxRevisionContentBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return RestoreContent{}, err
	}
	content.Size = int64(len(raw))
	if content.Size <= sourceledger.MaxRevisionContentBytes {
		content.Bytes = raw
	} else {
		n, err := io.Copy(hash, reader)
		if err != nil {
			return RestoreContent{}, err
		}
		content.Size += n
	}
	content.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return content, nil
}

func (r *restoreReview) validate(ctx context.Context, dir string) error {
	for path, expected := range r.inputs {
		current, err := readRestoreContent(path)
		if err != nil {
			return err
		}
		if current.Exists != expected.Exists || current.SHA256 != expected.SHA256 {
			return fmt.Errorf("git restore configuration changed during review")
		}
	}
	tree, err := restoreGit(ctx, dir, hermeticOpts(0), "write-tree")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(tree)) != r.indexTree {
		return fmt.Errorf("git index changed during restore review")
	}
	for _, file := range r.files {
		if file.IndexOnly {
			continue
		}
		current, err := readRestoreContent(filepath.Join(dir, file.Path))
		if err != nil {
			return err
		}
		if current.Exists != file.Before.Exists || current.Mode != file.Before.Mode || current.SHA256 != file.Before.SHA256 {
			return fmt.Errorf("file changed during restore review: %s", file.Path)
		}
	}
	return nil
}

func (r *restoreReview) captureInput(path string) error {
	if _, exists := r.inputs[path]; exists {
		return nil
	}
	content, err := readRestoreContent(path)
	if err != nil {
		return err
	}
	content.Bytes = nil
	r.inputs[path] = content
	return nil
}
