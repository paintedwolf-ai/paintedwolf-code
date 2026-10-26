package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/gitrepo"
)

func (p *preparedOperation) addPaths() []string {
	if p.request.Kind != "add" || len(p.request.Paths) == 0 {
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
	head, err := optionalObject(ctx, source, "HEAD^{commit}")
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
