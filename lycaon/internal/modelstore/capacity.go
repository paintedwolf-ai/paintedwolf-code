package modelstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

const installReserve = 2 << 30

// Retry and shared shards need space only for their missing bytes.
func (s *Store) admitInstall(ctx context.Context, m Manifest, resumable bool, beforeEviction func() error) error {
	need := SpaceRequirement{StagingBytes: manifestSize(m), UnpackingBytes: installReserve}
	if err := s.CheckSpace(need); !errors.Is(err, ErrInsufficientSpace) {
		return err
	}
	for _, a := range manifestFiles(m) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.reusableArtifact(ctx, a) {
			need.StagingBytes -= a.Bytes
			continue
		}
		if resumable {
			info, err := os.Lstat(filepath.Join(s.root, "partials", a.Digest))
			if err == nil && privatePartial(info) && info.Size() <= a.Bytes {
				need.StagingBytes -= info.Size()
			}
		}
	}
	if err := s.CheckSpace(need); !errors.Is(err, ErrInsufficientSpace) {
		return err
	}
	if beforeEviction != nil {
		if err := beforeEviction(); err != nil {
			return err
		}
	}
	if err := s.evictForInstall(ctx, m, need); err != nil {
		return err
	}
	if _, err := s.Collect(); err != nil {
		return err
	}
	return s.CheckSpace(need)
}

func (s *Store) evictForInstall(ctx context.Context, target Manifest, need SpaceRequirement) error {
	lock, err := s.lockJournal()
	if err != nil {
		return err
	}
	defer lock.Unlock()
	if err := s.recoverJournal(); err != nil {
		return err
	}
	h, err := s.history()
	if err != nil {
		return err
	}
	if len(h.visible) == 0 {
		return ErrInsufficientSpace
	}
	if err := s.checkEvictable(h.all, nil); err != nil {
		return err
	}
	reclaimed, err := s.reclaimableBytes(h.all, target)
	if err != nil {
		return err
	}
	needed, err := need.RequiredBytes()
	if err != nil {
		return err
	}
	if err := s.CheckSpace(SpaceRequirement{StagingBytes: max(0, needed-reclaimed)}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.appendRecord(journalRecord{Version: 2, Selection: "evicted"})
}

// Count blocks only when every hard link belongs to a revision being removed.
func (s *Store) reclaimableBytes(history []Manifest, target Manifest) (int64, error) {
	pinned := make(map[string]bool)
	for _, a := range manifestFiles(target) {
		pinned[a.Digest] = true
	}
	links := make(map[string]map[string]bool)
	for _, m := range history {
		for name, a := range manifestFiles(m) {
			if pinned[a.Digest] {
				continue
			}
			if links[a.Digest] == nil {
				links[a.Digest] = map[string]bool{}
			}
			links[a.Digest][filepath.Join(s.RevisionDir(m), name)] = true
		}
	}
	var total int64
	for digest, names := range links {
		info, err := os.Lstat(filepath.Join(s.root, s.artifactRel(digest)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		count := 1
		for name := range names {
			linked, err := os.Lstat(name)
			if err == nil && os.SameFile(info, linked) {
				count++
			}
		}
		total += reclaimableBlocks(info, count)
	}
	return total, nil
}
