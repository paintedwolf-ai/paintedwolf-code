package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
)

type gitClosureSessionKey struct{}

type fileDigestEntry struct {
	sum  string
	size int64
}

type verifiedStoreRepo struct {
	tmpRepo               string
	installedPacks        map[string]string
	verifiedReachableOIDs map[string]bool
}

type gitClosureStats struct {
	HashFileCalls   int
	HashBytesRead   int64
	StoreStageCalls int
}

type gitClosureSession struct {
	mu              sync.Mutex
	fileDigests     map[string]fileDigestEntry
	sharedDiskFiles map[string]map[string]archiveSource
	verifiedStores  map[string]*verifiedStoreRepo
	fileVisitState  map[string]uint8
	stats           *gitClosureStats
}

func newGitClosureSession(stats *gitClosureStats) *gitClosureSession {
	return &gitClosureSession{
		fileDigests:     make(map[string]fileDigestEntry),
		sharedDiskFiles: make(map[string]map[string]archiveSource),
		verifiedStores:  make(map[string]*verifiedStoreRepo),
		fileVisitState:  make(map[string]uint8),
		stats:           stats,
	}
}

func sessionFileVisitState(ctx context.Context) map[string]uint8 {
	if s := gitClosureSessionFromContext(ctx); s != nil {
		return s.fileVisitState
	}
	return make(map[string]uint8)
}

func gitClosureSessionFromContext(ctx context.Context) *gitClosureSession {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(gitClosureSessionKey{}).(*gitClosureSession)
	return s
}

func ensureGitClosureSession(ctx context.Context) (context.Context, func()) {
	if gitClosureSessionFromContext(ctx) != nil {
		return ctx, func() {}
	}
	s := newGitClosureSession(nil)
	return context.WithValue(ctx, gitClosureSessionKey{}, s), s.close
}

func withGitClosureStats(ctx context.Context) (context.Context, *gitClosureStats, func()) {
	stats := &gitClosureStats{}
	s := newGitClosureSession(stats)
	return context.WithValue(ctx, gitClosureSessionKey{}, s), stats, s.close
}

func (s *gitClosureSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, repo := range s.verifiedStores {
		if repo != nil && repo.tmpRepo != "" {
			_ = os.RemoveAll(repo.tmpRepo)
		}
		delete(s.verifiedStores, k)
	}
}

func sha256FileInSession(ctx context.Context, path string) (string, int64, error) {
	clean := filepath.Clean(path)
	s := gitClosureSessionFromContext(ctx)
	if s != nil {
		s.mu.Lock()
		if entry, ok := s.fileDigests[clean]; ok {
			s.mu.Unlock()
			return entry.sum, entry.size, nil
		}
		s.mu.Unlock()
	}
	sum, size, err := sha256File(clean)
	if err != nil {
		return "", 0, err
	}
	if s != nil {
		s.mu.Lock()
		s.fileDigests[clean] = fileDigestEntry{sum: sum, size: size}
		if s.stats != nil {
			s.stats.HashFileCalls++
			s.stats.HashBytesRead += size
		}
		s.mu.Unlock()
	}
	return sum, size, nil
}

func loadSharedDiskGitObjectFiles(ctx context.Context, configDir, storeDir, projKey, syntheticStorePrefix string) (map[string]archiveSource, error) {
	cacheKey := canonicalStoreIdentityOnDisk(configDir, storeDir) + "\x00" + projKey + "\x00" + syntheticStorePrefix
	s := gitClosureSessionFromContext(ctx)
	if s != nil {
		s.mu.Lock()
		if cached, ok := s.sharedDiskFiles[cacheKey]; ok {
			s.mu.Unlock()
			return cached, nil
		}
		s.mu.Unlock()
	}
	diskDirs := []struct{ dir, prefix string }{
		{filepath.Join(configDir, enginepaths.WorkerBranchesDirName, ".adapter", "privategit", "retained-store", "objects"), enginepaths.WorkerBranchesDirName + "/.adapter/privategit/retained-store/objects"},
		{filepath.Join(storeDir, "objects"), syntheticStorePrefix + "/objects"},
	}
	if projKey != "" {
		diskDirs = append(diskDirs, struct{ dir, prefix string }{
			filepath.Join(configDir, enginepaths.WorkerBranchesDirName, ".adapter", projKey, "retained-git-store", "objects"),
			enginepaths.WorkerBranchesDirName + "/.adapter/" + projKey + "/retained-git-store/objects",
		})
	}
	sharedFiles := make(map[string]archiveSource)
	for _, item := range diskDirs {
		if err := walkGitObjectsDirIntoFiles(item.dir, item.prefix, sharedFiles); err != nil {
			return nil, err
		}
	}
	if s != nil {
		s.mu.Lock()
		s.sharedDiskFiles[cacheKey] = sharedFiles
		s.mu.Unlock()
	}
	return sharedFiles, nil
}

func checkpointGitObjectPrefixes(branchRel, storePrefix, checkpointID string, pg checkpoint.PrivateGitSnapshot) []string {
	seen := make(map[string]bool)
	var prefixes []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			prefixes = append(prefixes, p)
		}
	}
	add(enginepaths.WorkerBranchesDirName + "/.adapter/privategit/retained-store/objects")
	add(storePrefix + "/objects")
	rootIDs := make([]string, 0, len(pg.Roots))
	for rootID := range pg.Roots {
		if isSafeCheckpointIdentity(rootID) {
			rootIDs = append(rootIDs, rootID)
		}
	}
	sort.Strings(rootIDs)
	addCheckpointPrefixes := func(cid string) {
		if !isSafeCheckpointIdentity(cid) {
			return
		}
		add(storePrefix + "/checkpoints/" + cid + "/git-objects")
		for _, rootID := range rootIDs {
			add(storePrefix + "/checkpoints/" + cid + "/git-objects-roots/" + rootID)
		}
	}
	addCheckpointPrefixes(checkpointID)
	for _, objCID := range pg.ObjectCheckpoints {
		addCheckpointPrefixes(objCID)
	}
	if projKey := branchProjKey(branchRel); projKey != "" {
		add(enginepaths.WorkerBranchesDirName + "/.adapter/" + projKey + "/retained-git-store/objects")
	}
	return prefixes
}

func computeStorePrefixesDigest(ctx context.Context, branchRel, storePrefix string, prefixes []string, files map[string]archiveSource) (string, bool) {
	var matchingRels []string
	for _, pfx := range prefixes {
		prefixSlash := pfx + "/"
		for rel := range files {
			if strings.HasPrefix(rel, prefixSlash) {
				matchingRels = append(matchingRels, rel)
			}
		}
	}
	sort.Strings(matchingRels)
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "proj:%s\x00prefix:%s\x00", branchProjKey(branchRel), storePrefix)
	for _, rel := range matchingRels {
		src := files[rel]
		if src.kind != fileKindRegular {
			return "", false
		}
		sum, size, err := sha256FileInSession(ctx, src.path)
		if err != nil {
			return "", false
		}
		_, _ = fmt.Fprintf(h, "file:%s:%d:%s\x00", rel, size, sum)
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

func stageAndVerifyStoreRepo(ctx context.Context, checkpointID string, prefixes []string, files map[string]archiveSource) (*verifiedStoreRepo, error) {
	tmpRepo, err := os.MkdirTemp("", "pw-backup-git-closure-*")
	if err != nil {
		return nil, err
	}
	cleanupOnErr := true
	defer func() {
		if cleanupOnErr {
			_ = os.RemoveAll(tmpRepo)
		}
	}()
	if out, code, err := gitexec.Run(ctx, tmpRepo, []string{"init", "--bare", "-q"}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); err != nil || code != 0 {
		return nil, &InvalidError{Detail: fmt.Sprintf("initialize git verification repo: %v (%s)", err, strings.TrimSpace(string(out)))}
	}
	installedPacks, err := stageClosureObjectsFromFiles(ctx, tmpRepo, prefixes, files)
	if err != nil {
		return nil, err
	}
	packBases := make([]string, 0, len(installedPacks))
	for base := range installedPacks {
		packBases = append(packBases, base)
	}
	sort.Strings(packBases)
	for _, base := range packBases {
		if out, code, err := gitexec.Run(ctx, tmpRepo, []string{"verify-pack", "-v", installedPacks[base]}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); err != nil || code != 0 {
			return nil, &InvalidError{Detail: fmt.Sprintf("git pack verification failed for %s (checkpoint %s): %s", base, checkpointID, strings.TrimSpace(string(out)))}
		}
	}
	if out, code, err := gitexec.Run(ctx, tmpRepo, []string{"fsck", "--full", "--no-dangling", "--strict"}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); err != nil || code != 0 {
		return nil, &InvalidError{Detail: fmt.Sprintf("git fsck failed for checkpoint %s: %s", checkpointID, strings.TrimSpace(string(out)))}
	}
	cleanupOnErr = false
	return &verifiedStoreRepo{
		tmpRepo:               tmpRepo,
		installedPacks:        installedPacks,
		verifiedReachableOIDs: make(map[string]bool),
	}, nil
}

func verifyCheckpointGitClosureInFiles(ctx context.Context, branchRel, storePrefix, checkpointID string, pg checkpoint.PrivateGitSnapshot, files map[string]archiveSource) error {
	ctx, closeClosureSession := ensureGitClosureSession(ctx)
	defer closeClosureSession()
	requiredOIDs, requiredPacks, err := collectRequiredGitOIDs(pg)
	if err != nil {
		return &InvalidError{Detail: fmt.Sprintf("checkpoint %s has invalid git metadata: %v", checkpointID, err)}
	}
	if len(requiredOIDs) == 0 && len(requiredPacks) == 0 {
		return nil
	}
	prefixes := checkpointGitObjectPrefixes(branchRel, storePrefix, checkpointID, pg)
	storeDigest, cacheable := computeStorePrefixesDigest(ctx, branchRel, storePrefix, prefixes, files)

	s := gitClosureSessionFromContext(ctx)
	var repo *verifiedStoreRepo
	if cacheable && s != nil {
		s.mu.Lock()
		repo = s.verifiedStores[storeDigest]
		s.mu.Unlock()
	}
	if repo == nil {
		staged, err := stageAndVerifyStoreRepo(ctx, checkpointID, prefixes, files)
		if err != nil {
			return err
		}
		if cacheable && s != nil {
			s.mu.Lock()
			if existing, ok := s.verifiedStores[storeDigest]; ok {
				_ = os.RemoveAll(staged.tmpRepo)
				repo = existing
			} else {
				s.verifiedStores[storeDigest] = staged
				if s.stats != nil {
					s.stats.StoreStageCalls++
				}
				repo = staged
			}
			s.mu.Unlock()
		} else {
			defer func() { _ = os.RemoveAll(staged.tmpRepo) }()
			repo = staged
		}
	}
	for _, reqPack := range requiredPacks {
		if repo.installedPacks[reqPack] == "" {
			return &InvalidError{Detail: fmt.Sprintf("retained git pack %s is missing for checkpoint %s", reqPack, checkpointID)}
		}
	}
	if len(requiredOIDs) == 0 {
		return nil
	}
	return verifyReachableOIDsInStoreRepo(ctx, s, repo, checkpointID, requiredOIDs)
}

func verifyReachableOIDsInStoreRepo(ctx context.Context, s *gitClosureSession, repo *verifiedStoreRepo, checkpointID string, requiredOIDs []string) error {
	var unverifiedTips []string
	if s != nil {
		s.mu.Lock()
		for _, oid := range requiredOIDs {
			if !repo.verifiedReachableOIDs[oid] {
				unverifiedTips = append(unverifiedTips, oid)
			}
		}
		s.mu.Unlock()
	} else {
		unverifiedTips = requiredOIDs
	}
	if len(unverifiedTips) == 0 {
		return nil
	}
	if err := verifyGitOIDsBatchCheck(ctx, repo.tmpRepo, checkpointID, unverifiedTips); err != nil {
		return err
	}
	stdinRevs := []byte(strings.Join(unverifiedTips, "\n") + "\n")
	out, code, err := gitexec.Run(ctx, repo.tmpRepo, []string{"rev-list", "--objects", "--stdin"}, gitexec.Opts{Profile: gitexec.ProfileHermetic, Input: stdinRevs})
	if err != nil || code != 0 {
		return &InvalidError{Detail: fmt.Sprintf("git rev-list closure failed for checkpoint %s: %s", checkpointID, strings.TrimSpace(string(out)))}
	}
	seenReachable := make(map[string]bool, len(unverifiedTips))
	var reachable []string
	addCandidate := func(oid string) {
		if seenReachable[oid] {
			return
		}
		if s != nil {
			s.mu.Lock()
			already := repo.verifiedReachableOIDs[oid]
			s.mu.Unlock()
			if already {
				return
			}
		}
		seenReachable[oid] = true
		reachable = append(reachable, oid)
	}
	for _, oid := range unverifiedTips {
		addCandidate(oid)
	}
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		fields := bytes.Fields(line)
		if len(fields) > 0 {
			oid := strings.ToLower(string(fields[0]))
			if isGitOID(oid) {
				addCandidate(oid)
			}
		}
	}
	if err := verifyGitOIDsBatchCheck(ctx, repo.tmpRepo, checkpointID, reachable); err != nil {
		return err
	}
	if s != nil {
		s.mu.Lock()
		for _, oid := range reachable {
			repo.verifiedReachableOIDs[oid] = true
		}
		s.mu.Unlock()
	}
	return nil
}

func stageClosureObjectsFromFiles(ctx context.Context, tmpRepo string, prefixes []string, files map[string]archiveSource) (map[string]string, error) {
	installedPacks := make(map[string]string)
	stagedSources := make(map[string]string)
	for _, objPrefix := range prefixes {
		prefixSlash := objPrefix + "/"
		packs := make(map[string]string)
		idxs := make(map[string]string)
		for rel, src := range files {
			if !strings.HasPrefix(rel, prefixSlash) {
				continue
			}
			if src.kind != fileKindRegular {
				return nil, &InvalidError{Detail: "non-regular git object entry: " + rel}
			}
			sub := strings.TrimPrefix(rel, prefixSlash)
			if strings.HasPrefix(sub, "pack/") {
				name := strings.TrimPrefix(sub, "pack/")
				if strings.Contains(name, "/") {
					return nil, &InvalidError{Detail: "invalid nested git pack entry: " + rel}
				}
				switch {
				case strings.HasSuffix(name, ".pack"):
					base := strings.TrimSuffix(name, ".pack")
					if !isValidGitPackBase(base) {
						return nil, &InvalidError{Detail: "invalid git pack name: " + rel}
					}
					packs[base] = src.path
				case strings.HasSuffix(name, ".idx"):
					base := strings.TrimSuffix(name, ".idx")
					if !isValidGitPackBase(base) {
						return nil, &InvalidError{Detail: "invalid git pack index name: " + rel}
					}
					idxs[base] = src.path
				case strings.HasSuffix(name, ".rev") || strings.HasSuffix(name, ".bitmap") || strings.HasSuffix(name, ".mtimes"):
				default:
					return nil, &InvalidError{Detail: "unexpected git pack entry: " + rel}
				}
				continue
			}
			parts := strings.Split(sub, "/")
			if len(parts) == 2 && len(parts[0]) == 2 && isHexOnly(parts[0]) && (len(parts[1]) == 38 || len(parts[1]) == 62) && isHexOnly(parts[1]) {
				dst := filepath.Join(tmpRepo, "objects", strings.ToLower(parts[0]), strings.ToLower(parts[1]))
				if err := linkOrCopyVerifyObject(ctx, src.path, dst, stagedSources); err != nil {
					return nil, &InvalidError{Detail: fmt.Sprintf("stage loose git object %s: %v", rel, err)}
				}
			}
		}
		for base, packPath := range packs {
			idxPath, ok := idxs[base]
			if !ok {
				return nil, &InvalidError{Detail: fmt.Sprintf("git pack %s in %s is missing matching .idx", base, objPrefix)}
			}
			dstPack := filepath.Join(tmpRepo, "objects", "pack", base+".pack")
			dstIdx := filepath.Join(tmpRepo, "objects", "pack", base+".idx")
			if err := linkOrCopyVerifyObject(ctx, packPath, dstPack, stagedSources); err != nil {
				return nil, &InvalidError{Detail: fmt.Sprintf("stage git pack %s: %v", base, err)}
			}
			if err := linkOrCopyVerifyObject(ctx, idxPath, dstIdx, stagedSources); err != nil {
				return nil, &InvalidError{Detail: fmt.Sprintf("stage git pack index %s: %v", base, err)}
			}
			installedPacks[base] = dstIdx
		}
		for base := range idxs {
			if packs[base] == "" {
				return nil, &InvalidError{Detail: fmt.Sprintf("git pack index %s in %s is missing matching .pack", base, objPrefix)}
			}
		}
	}
	return installedPacks, nil
}

func linkOrCopyVerifyObject(ctx context.Context, srcPath, dstPath string, stagedSources map[string]string) error {
	if prevSrc, ok := stagedSources[dstPath]; ok {
		sum1, size1, err1 := sha256FileInSession(ctx, prevSrc)
		sum2, size2, err2 := sha256FileInSession(ctx, srcPath)
		if err1 != nil || err2 != nil || sum1 != sum2 || size1 != size2 {
			return fmt.Errorf("conflicting duplicate git object content between %s and %s", prevSrc, srcPath)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o750); err != nil {
		return err
	}
	if err := os.Link(srcPath, dstPath); err == nil {
		stagedSources[dstPath] = srcPath
		return nil
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(dstPath),
		Source:   in,
		Mode:     0o444,
		DirMode:  0o750,
	})
	if err == nil {
		stagedSources[dstPath] = srcPath
	}
	return err
}

func verifyGitOIDsBatchCheck(ctx context.Context, tmpRepo, checkpointID string, oids []string) error {
	if len(oids) == 0 {
		return nil
	}
	input := strings.Join(oids, "\n") + "\n"
	out, code, err := gitexec.Run(ctx, tmpRepo, []string{"cat-file", "--batch-check"}, gitexec.Opts{
		Profile: gitexec.ProfileHermetic,
		Input:   []byte(input),
	})
	if err != nil || code != 0 {
		return &InvalidError{Detail: fmt.Sprintf("git cat-file check failed for checkpoint %s: %s", checkpointID, strings.TrimSpace(string(out)))}
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(oids) {
		return &InvalidError{Detail: fmt.Sprintf("git cat-file check line count mismatch for checkpoint %s", checkpointID)}
	}
	for i, oid := range oids {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, oid+" ") || strings.HasSuffix(line, " missing") {
			return &InvalidError{Detail: fmt.Sprintf("retained checkpoint git object is missing: %s (checkpoint %s)", oid, checkpointID)}
		}
	}
	return nil
}
