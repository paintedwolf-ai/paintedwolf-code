package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/gitexec"
)

// IndexEntry is one index change a "stage" operation makes: new content at
// stage 0, a removal, or an unmerged path's base, ours and theirs stages.
// The work tree is never touched.
type IndexEntry struct {
	Path string
	// Mode is the index mode for written stages: "100644", "100755" or "120000".
	Mode    string
	Content []byte
	// ObjectID reuses an existing blob without reading or rewriting its bytes.
	ObjectID string
	Remove   bool
	// Stages, when set, records a conflict: stages 1, 2 and 3 in order; a nil
	// side is absent from that stage.
	Stages *[3][]byte
}

// indexOnly reports kinds that change the index and never the work tree.
func (r OperationRequest) indexOnly() bool { return r.Kind == "add" || r.Kind == "stage" }

// stageEntries validates a stage request; its paths scope the rehearsal.
func (p *operationPlan) stageEntries() error {
	r := p.request
	if len(r.Entries) == 0 {
		return operationRefusal("explicit_paths_required")
	}
	paths := make([]string, 0, len(r.Entries))
	for _, entry := range r.Entries {
		if strings.ContainsRune(entry.Path, 0) {
			return operationRefusal("invalid_repository_path", entry.Path)
		}
		if !entry.Remove && entry.Mode != "100644" && entry.Mode != "100755" && entry.Mode != "120000" {
			return operationRefusal("invalid_index_mode", entry.Path)
		}
		if entry.ObjectID != "" && (!validObjectID(entry.ObjectID) || entry.Content != nil || entry.Stages != nil || entry.Remove) {
			return operationRefusal("invalid_index_object", entry.Path)
		}
		paths = append(paths, entry.Path)
	}
	if len(uniquePaths(paths)) != len(paths) {
		return operationRefusal("duplicate_repository_paths")
	}
	p.request.Paths = paths
	return validRepositoryPaths(paths, false)
}

// runStage writes each entry's blobs and replaces its index entries. Removed
// and conflicted paths leave stage 0 first, as update-index requires.
func (p *operationPlan) runStage(ctx context.Context, dir string) ([]byte, int, error) {
	var removals []string
	var info bytes.Buffer
	for _, entry := range p.request.Entries {
		if entry.Remove || entry.Stages != nil {
			removals = append(removals, entry.Path)
		}
		if entry.Remove {
			continue
		}
		if entry.ObjectID != "" {
			out, code, err := gitexec.Run(ctx, dir, []string{"--no-replace-objects", "cat-file", "-t", entry.ObjectID}, p.opts)
			if err != nil || code != 0 {
				return out, code, err
			}
			if strings.TrimSpace(string(out)) != "blob" {
				return []byte("index entry requires a blob object"), 1, nil
			}
			fmt.Fprintf(&info, "%s %s 0\t%s\x00", entry.Mode, entry.ObjectID, entry.Path)
			continue
		}
		if entry.Stages == nil {
			oid, out, code, err := p.writeBlob(ctx, dir, entry.Content)
			if err != nil || code != 0 {
				return out, code, err
			}
			fmt.Fprintf(&info, "%s %s 0\t%s\x00", entry.Mode, oid, entry.Path)
			continue
		}
		for i, side := range entry.Stages {
			if side == nil {
				continue
			}
			oid, out, code, err := p.writeBlob(ctx, dir, side)
			if err != nil || code != 0 {
				return out, code, err
			}
			fmt.Fprintf(&info, "%s %s %d\t%s\x00", entry.Mode, oid, i+1, entry.Path)
		}
	}
	if len(removals) > 0 {
		out, code, err := gitexec.Run(ctx, dir, append([]string{"update-index", "--force-remove", "--"}, removals...), p.opts)
		if err != nil || code != 0 || info.Len() == 0 {
			return out, code, err
		}
	}
	opts := p.opts
	opts.Input = info.Bytes()
	return gitexec.Run(ctx, dir, []string{"update-index", "-z", "--index-info"}, opts)
}

func (p *operationPlan) writeBlob(ctx context.Context, dir string, content []byte) (string, []byte, int, error) {
	opts := p.opts
	opts.Input = content
	if opts.Input == nil {
		opts.Input = []byte{}
	}
	var oid bytes.Buffer
	out, code, err := gitexec.RunTo(ctx, dir, []string{"hash-object", "-w", "--no-filters", "--stdin"}, opts, &oid)
	if err != nil || code != 0 {
		return "", out, code, err
	}
	id := strings.TrimSpace(oid.String())
	if !validObjectID(id) {
		return "", out, 1, fmt.Errorf("malformed object identity from hash-object")
	}
	return id, out, 0, nil
}

// MergeFile merges theirs into ours against base with Git's merge-file and
// returns the merged bytes and the number of conflicts it marked. labels
// name ours, base and theirs in the conflict markers.
func MergeFile(ctx context.Context, ours, base, theirs []byte, labels [3]string) ([]byte, int, error) {
	dir, err := os.MkdirTemp("", "git-merge-file-*")
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	names := [3]string{"ours", "base", "theirs"}
	for i, content := range [][]byte{ours, base, theirs} {
		if _, err := fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: dir, Rel: names[i]}, Source: bytes.NewReader(content), Mode: 0o600, DirMode: 0o700}); err != nil {
			return nil, 0, err
		}
	}
	var merged bytes.Buffer
	args := []string{"merge-file", "-p", "-L", labels[0], "-L", labels[1], "-L", labels[2], "--", names[0], names[1], names[2]}
	out, code, err := gitexec.RunTo(ctx, dir, args, hermeticOpts(0), &merged)
	if err != nil {
		return nil, 0, err
	}
	if code < 0 || code > 127 {
		return nil, 0, fmt.Errorf("merge-file failed: %s", bytes.TrimSpace(out))
	}
	return merged.Bytes(), code, nil
}

// metadataStage preserves blob identities while changing their index paths.
func (r OperationRequest) metadataStage() bool {
	if r.Kind != "stage" {
		return false
	}
	reused := false
	for _, entry := range r.Entries {
		if entry.Remove {
			continue
		}
		if entry.ObjectID == "" {
			return false
		}
		reused = true
	}
	return reused
}
