package sourcesnapshot

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/contextio"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/sourceblob"
)

// Bytes returns verified content from retained storage or the live file.
func (s *Store) Bytes(ctx context.Context, entry Entry) ([]byte, error) {
	if entry.SHA256 != "" && s.blobs != nil {
		if raw, err := s.blobs.GetSHA(entry.SHA256); err == nil && sourceblob.ContentSHA(raw) == entry.SHA256 {
			return raw, nil
		}
	}
	if entry.GitOID != "" {
		if raw, err := gitBlob(ctx, entry); err == nil {
			return raw, nil
		}
	}
	raw, err := readLiveVerified(ctx, entry)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrContentUnavailable, entry.Path)
	}
	return raw, nil
}

func gitBlob(ctx context.Context, entry Entry) ([]byte, error) {
	out, code, err := gitexec.Run(ctx, entry.RootPath, []string{"cat-file", "blob", entry.GitOID}, gitexec.Opts{
		Profile: gitexec.ProfileHermetic, MaxOutput: max(entry.Size+1<<16, exec.DefaultMaxOutputBytes),
	})
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("git cat-file exited %d", code)
	}
	if int64(len(out)) != entry.Size {
		return nil, fmt.Errorf("git object %s holds %d bytes, entry recorded %d", entry.GitOID, len(out), entry.Size)
	}
	return out, nil
}

// readLiveVerified checks the recorded digest, or metadata before and after reading.
func readLiveVerified(ctx context.Context, entry Entry) ([]byte, error) {
	abs := absPath(entry.RootPath, entry.Path)
	before, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if !entry.liveMetadataCompatible(before) {
		return nil, errChanged
	}
	file, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(contextio.Reader{Context: ctx, Source: file})
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !sameFileVersion(before, after) {
		return nil, errChanged
	}
	switch {
	case entry.GitOID != "":
		hasher := sourceblob.GitBlobSHA1(int64(len(raw)))
		hasher.Write(raw)
		if hex.EncodeToString(hasher.Sum(nil)) != entry.GitOID {
			return nil, errChanged
		}
	case entry.SHA256 != "":
		if sourceblob.ContentSHA(raw) != entry.SHA256 {
			return nil, errChanged
		}
	}
	return raw, nil
}

// Digest returns the recorded SHA256 or hashes the live file after verifying its metadata.
func (s *Store) Digest(ctx context.Context, entry Entry) (string, error) {
	if entry.SHA256 != "" {
		return entry.SHA256, nil
	}
	abs := absPath(entry.RootPath, entry.Path)
	before, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	if !entry.liveMetadataCompatible(before) {
		return "", fmt.Errorf("%w: %s", ErrContentUnavailable, entry.Path)
	}
	observed, err := hashFile(ctx, abs, before)
	if errors.Is(err, errChanged) {
		return "", fmt.Errorf("%w: %s", ErrContentUnavailable, entry.Path)
	}
	if err != nil {
		return "", err
	}
	if entry.ContentID() != "" && !observed.entry(entry.RootPath, entry.Path).SameContent(entry) {
		return "", fmt.Errorf("%w: %s", ErrContentUnavailable, entry.Path)
	}
	return observed.SHA256, nil
}

// A recorded digest allows content verification after a timestamp change.
func (e Entry) liveMetadataCompatible(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Size() == e.Size &&
		uint32(info.Mode()) == e.Mode && (e.ContentID() != "" || e.statMatches(info))
}

func (e Entry) liveContentMatches(ctx context.Context) bool {
	abs := absPath(e.RootPath, e.Path)
	info, err := os.Lstat(abs)
	if err != nil || !e.liveMetadataCompatible(info) {
		return false
	}
	if e.statMatches(info) {
		return true
	}
	observed, err := hashFile(ctx, abs, info)
	return err == nil && observed.entry(e.RootPath, e.Path).SameContent(e)
}

// MovedSince compares live content for the requested paths, or all admitted files.
// Matching metadata avoids a read; changed metadata requires the recorded digest.
func (s *Store) MovedSince(ctx context.Context, id string, paths []string) ([]string, error) {
	snapshot, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	var moved []string
	check := func(entry Entry) {
		if !entry.liveContentMatches(ctx) {
			moved = append(moved, entry.Path)
		}
	}
	if len(paths) == 0 {
		err := s.ForEachEntry(ctx, id, func(entry Entry) error {
			check(entry)
			return nil
		})
		return moved, err
	}
	for _, rel := range paths {
		for _, root := range snapshot.Roots {
			entry, ok, err := s.Lookup(ctx, id, root.Path, cleanRelDir(rel))
			if err != nil {
				return nil, err
			}
			if ok {
				check(entry)
				break
			}
		}
	}
	return moved, nil
}

// IsCurrent verifies the admitted file set and live content identities.
func (s *Store) IsCurrent(ctx context.Context, id string) (bool, error) {
	storageRelease, err := s.acquireStorage(ctx, false)
	if err != nil {
		return false, err
	}
	defer storageRelease()
	snapshot, err := s.Get(ctx, id)
	if err != nil {
		return false, err
	}
	rootRelease, err := s.acquireRootStorage(ctx, snapshot.Roots, false)
	if err != nil {
		return false, err
	}
	defer rootRelease()
	if _, err := s.Get(ctx, id); err != nil {
		return false, err
	}
	admitted := 0
	for _, root := range snapshot.Roots {
		if _, err := admittedFiles(ctx, root, s.scopeFor(ctx, Request{}, root), func(fileRef) error {
			admitted++
			return nil
		}); err != nil {
			return false, err
		}
	}
	if admitted != snapshot.FileCount {
		return false, nil
	}
	baselines := make(map[string]*observed, len(snapshot.Roots))
	ids := make(map[string]*identifier, len(snapshot.Roots))
	defer func() {
		for _, baseline := range baselines {
			baseline.close()
		}
	}()
	for _, root := range snapshot.Roots {
		baseline, err := s.newObserved(ctx, root.Path)
		if err != nil {
			return false, err
		}
		baselines[root.Path] = baseline
		ids[root.Path] = &identifier{root: root.Path}
	}
	current := true
	err = s.ForEachEntry(ctx, id, func(entry Entry) error {
		abs := absPath(entry.RootPath, entry.Path)
		info, err := os.Lstat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				current = false
				return errStop
			}
			return err
		}
		if entry.statMatches(info) {
			return nil
		}
		live, err := s.identify(ctx, baselines[entry.RootPath], ids[entry.RootPath], fileRef{Path: entry.Path, Abs: abs}, VerifyStat)
		if err != nil || !live.SameContent(entry) {
			current = false
			return errStop
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStop) {
		return false, err
	}
	for _, baseline := range baselines {
		if err := baseline.flush(ctx, s); err != nil {
			return false, err
		}
	}
	return current, nil
}

var errStop = errors.New("stop")
