package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitrepo"
)

func (p *preparedOperation) addPaths() []string {
	if !p.request.indexOnly() || len(p.request.Paths) == 0 {
		return nil
	}
	for _, path := range p.request.Paths {
		if filepath.Clean(path) == "." {
			return nil
		}
	}
	return p.request.Paths
}

func (p *preparedOperation) statusArgs() []string {
	args := []string{"status", "--porcelain=v1", "-z", "--untracked-files=all"}
	if paths := p.addPaths(); len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	return args
}

func (p *preparedOperation) copyNestedRepo(ctx context.Context, root, scratch, path string) error {
	clean := filepath.Clean(path)
	source := filepath.Join(root, clean)
	nested, ok := gitrepo.Discover(source)
	if !ok || nested.Root != source {
		return operationRefusal("unsupported_file_kind", clean)
	}
	head, err := ResolveOptionalObject(ctx, source, "HEAD^{commit}")
	if err != nil {
		return err
	}
	p.nestedHeads[clean] = head
	if _, err := restoreGit(ctx, p.scratch, p.opts, "clone", "--shared", "--no-checkout", "--no-hardlinks", "--", source, filepath.Join(scratch, clean)); err != nil {
		return operationRefusal("nested_repository_unavailable", clean)
	}
	return nil
}

func (p *preparedOperation) indexContent(ctx context.Context, scratch, path string, entry operationIndexEntry) (RestoreContent, error) {
	if p.request.metadataStage() && entry.oid != "" {
		out, code, err := gitexec.Run(ctx, scratch, []string{"--no-replace-objects", "cat-file", "-s", entry.oid}, hermeticOpts(0))
		if err != nil {
			return RestoreContent{}, err
		}
		if code != 0 {
			return RestoreContent{}, fmt.Errorf("read index object size: %s", out)
		}
		size, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		if err != nil || size < 0 {
			return RestoreContent{}, fmt.Errorf("invalid index object size")
		}
		return RestoreContent{Exists: true, Mode: entry.mode, Size: size, ObjectID: entry.oid}, nil
	}
	if entry.mode.IsDir() {
		if p.request.Kind != "add" {
			return RestoreContent{}, operationRefusal("submodule_change_unsupported", path)
		}
		if _, nested := p.nestedHeads[path]; !nested {
			return RestoreContent{}, operationRefusal("submodule_change_unsupported", path)
		}
		content := []byte(entry.oid + "\n")
		sum := sha256.Sum256(content)
		return RestoreContent{Exists: true, Mode: entry.mode, Bytes: content, Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}, nil
	}
	return readOperationBlob(ctx, scratch, entry)
}
