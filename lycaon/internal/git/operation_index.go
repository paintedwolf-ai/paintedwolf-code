package git

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/sourceledger"
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
	if len(paths) == 0 && opts.IndexFile == "" && ctx.Err() == nil {
		if fast, ok := readOperationIndexFast(dir); ok {
			return fast, nil
		}
	}
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
		fileMode := indexModeToFileMode(uint32(mode))
		entries[operationIndexKey{path, stage}] = operationIndexEntry{fields[1], fileMode}
		if len(entries) > 100000 {
			return operationRefusal("repository_entry_limit")
		}
		return nil
	})
	return entries, err
}

func indexModeToFileMode(mode uint32) os.FileMode {
	fileMode := os.FileMode(mode & 0o777)
	switch mode & 0o170000 {
	case 0o120000:
		fileMode |= os.ModeSymlink
	case 0o160000:
		fileMode |= os.ModeDir
	}
	return gitFileMode(fileMode)
}

func readOperationIndexFast(dir string) (operationIndex, bool) {
	repo, ok := gitrepo.Discover(dir)
	if !ok || repo.GitDir == "" {
		return nil, false
	}
	if shared, err := filepath.Glob(filepath.Join(repo.GitDir, "sharedindex.*")); err != nil || len(shared) > 0 {
		return nil, false
	}
	raw, err := os.ReadFile(filepath.Join(repo.GitDir, "index"))
	if os.IsNotExist(err) {
		return operationIndex{}, true
	}
	if err != nil || len(raw) < 32 || string(raw[:4]) != "DIRC" {
		return nil, false
	}
	version := binary.BigEndian.Uint32(raw[4:8])
	count := binary.BigEndian.Uint32(raw[8:12])
	if version != 2 || count > 100000 {
		return nil, false
	}
	end := len(raw) - 20
	pos := 12
	entries := make(operationIndex, count)
	for i := uint32(0); i < count; i++ {
		if pos+62 > end {
			return nil, false
		}
		mode := binary.BigEndian.Uint32(raw[pos+24 : pos+28])
		oid := hex.EncodeToString(raw[pos+40 : pos+60])
		flags := binary.BigEndian.Uint16(raw[pos+60 : pos+62])
		if flags&0x4000 != 0 {
			return nil, false
		}
		nul := bytes.IndexByte(raw[pos+62:end], 0)
		if nul <= 0 {
			return nil, false
		}
		if nameLen := int(flags & 0x0fff); nameLen < 0x0fff && nul != nameLen {
			return nil, false
		}
		pathBytes := raw[pos+62 : pos+62+nul]
		if _, err := parseRepoPath(pathBytes); err != nil {
			return nil, false
		}
		stage := int((flags >> 12) & 0x3)
		entries[operationIndexKey{string(pathBytes), stage}] = operationIndexEntry{oid, indexModeToFileMode(mode)}
		pos += (62 + nul + 8) &^ 7
	}
	return entries, pos <= end
}

type cachedBlobContent struct {
	raw    []byte
	size   int64
	sha256 string
}

var operationBlobCache sync.Map // string -> cachedBlobContent

func readLooseGitObject(dir, oid string) (string, []byte, bool) {
	if !validObjectID(oid) {
		return "", nil, false
	}
	repo, ok := gitrepo.Discover(dir)
	if !ok || repo.CommonDir == "" {
		return "", nil, false
	}
	objDirs := []string{filepath.Join(repo.CommonDir, "objects")}
	if altRaw, err := os.ReadFile(filepath.Join(repo.CommonDir, "objects", "info", "alternates")); err == nil {
		for _, line := range strings.Split(string(altRaw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if !filepath.IsAbs(line) {
				line = filepath.Join(repo.CommonDir, "objects", line)
			}
			objDirs = append(objDirs, filepath.Clean(line))
		}
	}
	for _, objDir := range objDirs {
		f, err := os.Open(filepath.Join(objDir, oid[:2], oid[2:]))
		if err != nil {
			continue
		}
		zr, err := zlib.NewReader(f)
		if err != nil {
			_ = f.Close()
			return "", nil, false
		}
		raw, err := io.ReadAll(io.LimitReader(zr, sourceledger.MaxRevisionContentBytes+1024))
		_ = zr.Close()
		_ = f.Close()
		if err != nil || int64(len(raw)) > sourceledger.MaxRevisionContentBytes+512 {
			return "", nil, false
		}
		headerBytes, body, found := bytes.Cut(raw, []byte{0})
		if !found {
			return "", nil, false
		}
		kind, sizeStr, found := strings.Cut(string(headerBytes), " ")
		if !found {
			return "", nil, false
		}
		size, err := strconv.ParseInt(sizeStr, 10, 64)
		if err != nil || size != int64(len(body)) {
			return "", nil, false
		}
		return kind, body, true
	}
	return "", nil, false
}

func readOperationBlob(ctx context.Context, dir string, entry operationIndexEntry) (RestoreContent, error) {
	if entry.oid == "" {
		return RestoreContent{}, nil
	}
	if entry.mode.IsDir() {
		return RestoreContent{}, operationRefusal("submodule_change_unsupported")
	}
	if validObjectID(entry.oid) && ctx.Err() == nil {
		if cached, ok := operationBlobCache.Load(entry.oid); ok {
			b := cached.(cachedBlobContent)
			return RestoreContent{Exists: true, Mode: entry.mode, Bytes: b.raw, Size: b.size, SHA256: b.sha256}, nil
		}
		if kind, body, ok := readLooseGitObject(dir, entry.oid); ok && kind == "blob" {
			sum := sha256.Sum256(body)
			hexSum := hex.EncodeToString(sum[:])
			var stored []byte
			if int64(len(body)) <= sourceledger.MaxRevisionContentBytes {
				stored = body
			}
			if len(body) <= 65536 {
				operationBlobCache.Store(entry.oid, cachedBlobContent{raw: stored, size: int64(len(body)), sha256: hexSum})
			}
			return RestoreContent{Exists: true, Mode: entry.mode, Bytes: stored, Size: int64(len(body)), SHA256: hexSum}, nil
		}
	}
	writer := &restoreBlobCapture{hash: sha256.New()}
	out, code, err := gitexec.RunTo(ctx, dir, []string{"--no-replace-objects", "cat-file", "blob", "--end-of-options", entry.oid}, hermeticOpts(0), writer)
	if err != nil {
		return RestoreContent{}, err
	}
	if code != 0 {
		return RestoreContent{}, fmt.Errorf("read index blob: %s", out)
	}
	hexSum := hex.EncodeToString(writer.hash.Sum(nil))
	if validObjectID(entry.oid) && writer.size <= 65536 {
		operationBlobCache.Store(entry.oid, cachedBlobContent{raw: writer.raw, size: writer.size, sha256: hexSum})
	}
	return RestoreContent{Exists: true, Mode: entry.mode, Bytes: writer.raw, Size: writer.size, SHA256: hexSum}, nil
}

func diffOperationIndexPaths(before, after operationIndex) []string {
	var paths []string
	for key, entry := range before {
		if after[key] != entry {
			paths = append(paths, key.path)
		}
	}
	for key, entry := range after {
		if before[key] != entry {
			paths = append(paths, key.path)
		}
	}
	return paths
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
			old, err := p.indexContent(ctx, scratch, path, before[key])
			if err != nil {
				return err
			}
			next, err := p.indexContent(ctx, scratch, path, after[key])
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
