package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
)

type operationIndexKey struct {
	path  string
	stage int
}
type operationIndexEntry struct {
	oid  string
	mode os.FileMode
}
type operationIndex map[operationIndexKey]operationIndexEntry

// Conflict resolution replaces stages 1–3 with stage 0, even when file bytes match.
func readOperationIndex(ctx context.Context, dir string, opts gitexec.Opts, paths ...string) (operationIndex, error) {
	entries := operationIndex{}
	args := append([]string{"--literal-pathspecs", "ls-files", "--stage", "-z", "--"}, paths...)
	err := gitexec.RunRecords(ctx, dir, args, opts, func(raw []byte) error {
		meta, path, ok := strings.Cut(string(raw), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || !validObjectID(fields[1]) {
			return fmt.Errorf("malformed Git index entry")
		}
		if _, err := parseRepoPath([]byte(path)); err != nil {
			return err
		}
		mode, err := strconv.ParseUint(fields[0], 8, 32)
		if err != nil {
			return err
		}
		stage, err := strconv.Atoi(fields[2])
		if err != nil || stage < 0 || stage > 3 {
			return fmt.Errorf("invalid Git index stage")
		}
		fileMode := os.FileMode(mode & 0o777)
		switch mode & 0o170000 {
		case 0o120000:
			fileMode |= os.ModeSymlink
		case 0o160000:
			fileMode |= os.ModeDir
		}
		fileMode = gitFileMode(fileMode)
		entries[operationIndexKey{path, stage}] = operationIndexEntry{fields[1], fileMode}
		if len(entries) > 100000 {
			return operationRefusal("repository_entry_limit")
		}
		return nil
	})
	return entries, err
}

func readOperationBlob(ctx context.Context, dir string, entry operationIndexEntry) (RestoreContent, error) {
	if entry.oid == "" {
		return RestoreContent{}, nil
	}
	if entry.mode.IsDir() {
		return RestoreContent{}, operationRefusal("submodule_change_unsupported")
	}
	writer := &restoreBlobCapture{hash: sha256.New()}
	out, code, err := gitexec.RunTo(ctx, dir, []string{"--no-replace-objects", "cat-file", "blob", "--end-of-options", entry.oid}, hermeticOpts(0), writer)
	if err != nil {
		return RestoreContent{}, err
	}
	if code != 0 {
		return RestoreContent{}, fmt.Errorf("read index blob: %s", out)
	}
	return RestoreContent{Exists: true, Mode: entry.mode, Bytes: writer.raw, Size: writer.size, SHA256: hex.EncodeToString(writer.hash.Sum(nil))}, nil
}

func (p *preparedOperation) prepareIndexFiles(ctx context.Context, scratch string, before, after operationIndex) error {
	paths := []string{}
	for key := range before {
		paths = append(paths, key.path)
	}
	for key := range after {
		paths = append(paths, key.path)
	}
	for _, path := range uniquePaths(paths) {
		for stage := 0; stage <= 3; stage++ {
			key := operationIndexKey{path, stage}
			if before[key] == after[key] {
				continue
			}
			old, err := readOperationBlob(ctx, scratch, before[key])
			if err != nil {
				return err
			}
			next, err := readOperationBlob(ctx, scratch, after[key])
			if err != nil {
				return err
			}
			if err := p.appendFile(RestoreFile{Path: path, IndexOnly: true, IndexStage: stage, Before: old, After: next}); err != nil {
				return err
			}
		}
	}
	p.expectedIndex = after
	return nil
}

func (p *preparedOperation) verifyIndex(ctx context.Context, root string) error {
	actual, err := readOperationIndex(ctx, root, hermeticOpts(0))
	if err != nil {
		return err
	}
	if len(actual) != len(p.expectedIndex) {
		return fmt.Errorf("index differs from reviewed Git result")
	}
	for key, entry := range p.expectedIndex {
		if actual[key] != entry {
			return fmt.Errorf("index differs from reviewed Git result: %s", key.path)
		}
	}
	return nil
}
