package sourcesnapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"

	"github.com/lycaon/lycaon/internal/contextio"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourceobservation"
)

const (
	// observationFlushEvery bounds uncached capture work after interruption.
	observationFlushEvery = 512
	// Files above maxHashBytes use index identities or stat metadata.
	maxHashBytes = 64 << 20
	// maxFileCaptureAttempts bounds re-reads of a file moving under the read.
	maxFileCaptureAttempts = 3
	// gitIndexDeltaThreshold amortizes index loading across changed files.
	gitIndexDeltaThreshold = 256
)

var (
	errVanished = errors.New("source file left the tree during capture")
	errUnstable = errors.New("source file never held still long enough to capture")
	errSkipped  = errors.New("source path is not a regular file")
	errChanged  = errors.New("source file changed under the read")
)

// observation binds verified content digests to file metadata.
type observation struct {
	SHA256     string
	GitOID     string
	Size       int64
	Mode       uint32
	ModifiedNS int64
}

func (o observation) matches(info os.FileInfo) bool {
	return (o.SHA256 != "" || o.GitOID != "") && info != nil &&
		info.Size() == o.Size &&
		uint32(info.Mode()) == o.Mode &&
		info.ModTime().UnixNano() == o.ModifiedNS
}

func (o observation) entry(rootPath, rel string) Entry {
	identity := IdentityHashed
	if o.SHA256 == "" {
		identity = IdentityIndex
	}
	return Entry{
		RootPath: rootPath, Path: rel, SHA256: o.SHA256, GitOID: o.GitOID, Identity: identity,
		Size: o.Size, Mode: o.Mode, ModifiedNS: o.ModifiedNS,
	}
}

// observed reads cached identities on demand and batches new observations.
type observed struct {
	rootPath string
	reader   *sourceobservation.Reader
	pending  map[string]observation
	hits     int
	reads    int
}

func (s *Store) newObserved(ctx context.Context, rootPath string) (*observed, error) {
	reader, err := s.observations.Reader(ctx)
	if err != nil {
		return nil, err
	}
	return &observed{
		rootPath: rootPath, reader: reader,
		pending: make(map[string]observation, observationFlushEvery),
	}, nil
}

func (o *observed) prior(ctx context.Context, rel string) (observation, bool) {
	if obs, ok := o.pending[rel]; ok {
		return obs, true
	}
	row, ok, err := o.reader.Get(ctx, o.rootPath, rel)
	if err != nil || !ok {
		return observation{}, false
	}
	mode, err := fileModeFromDB(row.Mode)
	if err != nil {
		return observation{}, false
	}
	return observation{SHA256: row.SHA256, GitOID: row.GitOID, Size: row.Size, Mode: mode, ModifiedNS: row.ModifiedNS}, true
}

func (o *observed) record(rel string, obs observation) {
	o.pending[rel] = obs
}

func (o *observed) flush(ctx context.Context, s *Store) error {
	if len(o.pending) == 0 {
		return nil
	}
	files := make([]sourceobservation.File, 0, len(o.pending))
	for rel, obs := range o.pending {
		files = append(files, sourceobservation.File{
			Path: rel, SHA256: obs.SHA256, GitOID: obs.GitOID, Size: obs.Size,
			Mode: int64(obs.Mode), ModifiedNS: obs.ModifiedNS,
		})
	}
	if err := s.observations.Put(ctx, o.rootPath, files); err != nil {
		return err
	}
	clear(o.pending)
	return nil
}

func (o *observed) close() {
	if o != nil && o.reader != nil {
		_ = o.reader.Close()
		o.reader = nil
	}
}

// identifier loads the root index once when enough paths need it.
type identifier struct {
	root   string
	index  *gitIndex
	loaded bool
	asked  int
	// Whole-root surveys load the index on the first lookup.
	eager bool
}

func (i *identifier) lookup(ctx context.Context, rel string) (string, bool) {
	i.asked++
	if !i.loaded && (i.eager || i.asked > gitIndexDeltaThreshold) {
		i.loaded = true
		index, err := readGitIndex(ctx, i.root)
		if err != nil {
			slog.InfoContext(ctx, "git index unavailable; files are identified by reading them", "root", i.root, "error", err)
		}
		i.index = index
	}
	return i.index.lookup(rel)
}

// identify reuses verified identities or hashes files within the read limit.
func (s *Store) identify(ctx context.Context, base *observed, ids *identifier, ref fileRef, verify Verify) (Entry, error) {
	info, err := ref.stat()
	if err != nil {
		if os.IsNotExist(err) {
			return Entry{}, errVanished
		}
		return Entry{}, err
	}
	if !info.Mode().IsRegular() {
		return Entry{}, errSkipped
	}
	if verify != VerifyContent {
		if oid, ok := ids.lookup(ctx, ref.Path); ok {
			base.hits++
			return Entry{
				RootPath: base.rootPath, Path: ref.Path, GitOID: oid, Identity: IdentityIndex,
				Size: info.Size(), Mode: uint32(info.Mode()), ModifiedNS: info.ModTime().UnixNano(),
			}, nil
		}
		if prior, ok := base.prior(ctx, ref.Path); ok && prior.matches(info) {
			base.hits++
			return prior.entry(base.rootPath, ref.Path), nil
		}
	}
	if info.Size() > maxHashBytes {
		return Entry{
			RootPath: base.rootPath, Path: ref.Path, Identity: IdentityStat,
			Size: info.Size(), Mode: uint32(info.Mode()), ModifiedNS: info.ModTime().UnixNano(),
		}, nil
	}
	if s.onCapture != nil {
		s.onCapture(ref.Abs)
	}
	for range maxFileCaptureAttempts {
		obs, err := hashFile(ctx, ref.Abs, info)
		switch {
		case errors.Is(err, errChanged):
			base.reads++
			info, err = os.Lstat(ref.Abs)
			if err != nil {
				if os.IsNotExist(err) {
					return Entry{}, errVanished
				}
				return Entry{}, err
			}
			if !info.Mode().IsRegular() {
				return Entry{}, errSkipped
			}
			continue
		case os.IsNotExist(err):
			return Entry{}, errVanished
		case err != nil:
			return Entry{}, err
		}
		base.reads++
		base.record(ref.Path, obs)
		return obs.entry(base.rootPath, ref.Path), nil
	}
	// A prior version keeps a moving file represented.
	if prior, ok := base.prior(ctx, ref.Path); ok {
		return prior.entry(base.rootPath, ref.Path), nil
	}
	return Entry{}, errUnstable
}

// hashFile computes both digests in one pass and rejects concurrent changes.
func hashFile(ctx context.Context, abs string, before os.FileInfo) (observation, error) {
	file, err := os.Open(abs)
	if err != nil {
		return observation{}, err
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	// The blob header includes the expected byte count.
	gitSHA1 := sourceblob.GitBlobSHA1(before.Size())
	copied, err := io.Copy(io.MultiWriter(digest, gitSHA1), contextio.Reader{Context: ctx, Source: file})
	if err != nil {
		return observation{}, err
	}
	after, err := file.Stat()
	if err != nil {
		return observation{}, err
	}
	if copied != before.Size() || !sameFileVersion(before, after) {
		return observation{}, errChanged
	}
	return observation{
		SHA256: hex.EncodeToString(digest.Sum(nil)), GitOID: hex.EncodeToString(gitSHA1.Sum(nil)),
		Size: after.Size(), Mode: uint32(after.Mode()), ModifiedNS: after.ModTime().UnixNano(),
	}, nil
}

func sameFileVersion(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) &&
		left.Size() == right.Size() && left.Mode() == right.Mode() &&
		left.ModTime().Equal(right.ModTime())
}
