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

var (
	verifiedGitClosureMu    sync.Mutex
	verifiedGitClosureByKey = make(map[string]string)
	verifiedGitClosureSet   = make(map[string]struct{})
)

const maxVerifiedGitClosureCacheEntries = 512

func computeGitClosureCacheFingerprints(branchRel, storePrefix, checkpointID string, requiredOIDs, requiredPacks, prefixes []string, files map[string]archiveSource) (string, string, bool) {
	var matchingRels []string
	var storeRoots []string
	seenRoots := make(map[string]bool)
	for _, pfx := range prefixes {
		prefixSlash := pfx + "/"
		for rel, src := range files {
			if !strings.HasPrefix(rel, prefixSlash) {
				continue
			}
			matchingRels = append(matchingRels, rel)
			suffix := filepath.FromSlash(rel)
			if cleaned := filepath.Clean(src.path); strings.HasSuffix(cleaned, suffix) {
				root := filepath.Clean(strings.TrimSuffix(cleaned, suffix))
				if realRoot, err := filepath.EvalSymlinks(root); err == nil {
					root = realRoot
				}
				if !seenRoots[root] {
					seenRoots[root] = true
					storeRoots = append(storeRoots, root)
				}
			}
		}
	}
	sort.Strings(matchingRels)
	sort.Strings(storeRoots)

	h := sha256.New()
	_, _ = fmt.Fprintf(h, "proj:%s\x00prefix:%s\x00cp:%s\x00", branchProjKey(branchRel), storePrefix, checkpointID)
	for _, oid := range requiredOIDs {
		_, _ = fmt.Fprintf(h, "oid:%s\x00", oid)
	}
	for _, pack := range requiredPacks {
		_, _ = fmt.Fprintf(h, "pack:%s\x00", pack)
	}
	for _, rel := range matchingRels {
		src := files[rel]
		if src.kind != fileKindRegular {
			return "", "", false
		}
		sum, size, err := sha256File(src.path)
		if err != nil {
			return "", "", false
		}
		_, _ = fmt.Fprintf(h, "file:%s:%d:%s\x00", rel, size, sum)
	}
	digest := hex.EncodeToString(h.Sum(nil))
	storeKey := strings.Join(storeRoots, "|") + "\x00" + branchProjKey(branchRel) + "\x00" + checkpointID
	return storeKey, digest, true
}

func checkVerifiedGitClosureCache(storeKey, digest string) bool {
	verifiedGitClosureMu.Lock()
	defer verifiedGitClosureMu.Unlock()
	if prev, ok := verifiedGitClosureByKey[storeKey]; ok && prev == digest {
		return true
	}
	_, ok := verifiedGitClosureSet[digest]
	return ok
}

func recordVerifiedGitClosureCache(storeKey, digest string) {
	verifiedGitClosureMu.Lock()
	defer verifiedGitClosureMu.Unlock()
	if len(verifiedGitClosureSet) >= maxVerifiedGitClosureCacheEntries {
		clear(verifiedGitClosureByKey)
		clear(verifiedGitClosureSet)
	}
	verifiedGitClosureByKey[storeKey] = digest
	verifiedGitClosureSet[digest] = struct{}{}
}

func checkpointGitObjectPrefixes(branchRel, storePrefix, checkpointID string) []string {
	prefixes := []string{
		enginepaths.WorkerBranchesDirName + "/.adapter/privategit/retained-store/objects",
		storePrefix + "/objects",
		storePrefix + "/checkpoints/" + checkpointID + "/git-objects",
	}
	if projKey := branchProjKey(branchRel); projKey != "" {
		prefixes = append(prefixes, enginepaths.WorkerBranchesDirName+"/.adapter/"+projKey+"/retained-git-store/objects")
	}
	return prefixes
}

func verifyCheckpointGitClosureInFiles(ctx context.Context, branchRel, storePrefix, checkpointID string, pg checkpoint.PrivateGitSnapshot, files map[string]archiveSource) error {
	requiredOIDs, requiredPacks, err := collectRequiredGitOIDs(pg)
	if err != nil {
		return &InvalidError{Detail: fmt.Sprintf("checkpoint %s has invalid git metadata: %v", checkpointID, err)}
	}
	if len(requiredOIDs) == 0 && len(requiredPacks) == 0 {
		return nil
	}
	prefixes := checkpointGitObjectPrefixes(branchRel, storePrefix, checkpointID)
	storeKey, closureDigest, cacheable := computeGitClosureCacheFingerprints(branchRel, storePrefix, checkpointID, requiredOIDs, requiredPacks, prefixes, files)
	if cacheable && checkVerifiedGitClosureCache(storeKey, closureDigest) {
		return nil
	}
	tmpRepo, err := os.MkdirTemp("", "pw-backup-git-closure-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmpRepo) }()

	if out, code, err := gitexec.Run(ctx, tmpRepo, []string{"init", "--bare", "-q"}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); err != nil || code != 0 {
		return &InvalidError{Detail: fmt.Sprintf("initialize git verification repo: %v (%s)", err, strings.TrimSpace(string(out)))}
	}
	installedPacks, err := stageClosureObjectsFromFiles(tmpRepo, prefixes, files)
	if err != nil {
		return err
	}
	for _, reqPack := range requiredPacks {
		if installedPacks[reqPack] == "" {
			return &InvalidError{Detail: fmt.Sprintf("retained git pack %s is missing for checkpoint %s", reqPack, checkpointID)}
		}
	}
	packBases := make([]string, 0, len(installedPacks))
	for base := range installedPacks {
		packBases = append(packBases, base)
	}
	sort.Strings(packBases)
	for _, base := range packBases {
		if out, code, err := gitexec.Run(ctx, tmpRepo, []string{"verify-pack", "-v", installedPacks[base]}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); err != nil || code != 0 {
			return &InvalidError{Detail: fmt.Sprintf("git pack verification failed for %s (checkpoint %s): %s", base, checkpointID, strings.TrimSpace(string(out)))}
		}
	}
	if out, code, err := gitexec.Run(ctx, tmpRepo, []string{"fsck", "--full", "--no-dangling", "--strict"}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); err != nil || code != 0 {
		return &InvalidError{Detail: fmt.Sprintf("git fsck failed for checkpoint %s: %s", checkpointID, strings.TrimSpace(string(out)))}
	}
	if len(requiredOIDs) == 0 {
		if cacheable {
			recordVerifiedGitClosureCache(storeKey, closureDigest)
		}
		return nil
	}
	if err := verifyGitOIDsBatchCheck(ctx, tmpRepo, checkpointID, requiredOIDs); err != nil {
		return err
	}
	stdinRevs := []byte(strings.Join(requiredOIDs, "\n") + "\n")
	out, code, err := gitexec.Run(ctx, tmpRepo, []string{"rev-list", "--objects", "--stdin"}, gitexec.Opts{Profile: gitexec.ProfileHermetic, Input: stdinRevs})
	if err != nil || code != 0 {
		return &InvalidError{Detail: fmt.Sprintf("git rev-list closure failed for checkpoint %s: %s", checkpointID, strings.TrimSpace(string(out)))}
	}
	seenReachable := make(map[string]bool, len(requiredOIDs))
	reachable := make([]string, 0, len(requiredOIDs))
	for _, oid := range requiredOIDs {
		seenReachable[oid] = true
		reachable = append(reachable, oid)
	}
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		fields := bytes.Fields(line)
		if len(fields) > 0 {
			oid := strings.ToLower(string(fields[0]))
			if isGitOID(oid) && !seenReachable[oid] {
				seenReachable[oid] = true
				reachable = append(reachable, oid)
			}
		}
	}
	if err := verifyGitOIDsBatchCheck(ctx, tmpRepo, checkpointID, reachable); err != nil {
		return err
	}
	if cacheable {
		recordVerifiedGitClosureCache(storeKey, closureDigest)
	}
	return nil
}

func stageClosureObjectsFromFiles(tmpRepo string, prefixes []string, files map[string]archiveSource) (map[string]string, error) {
	installedPacks := make(map[string]string)
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
				if err := linkOrCopyVerifyObject(src.path, dst); err != nil {
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
			if err := linkOrCopyVerifyObject(packPath, dstPack); err != nil {
				return nil, &InvalidError{Detail: fmt.Sprintf("stage git pack %s: %v", base, err)}
			}
			if err := linkOrCopyVerifyObject(idxPath, dstIdx); err != nil {
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

func linkOrCopyVerifyObject(srcPath, dstPath string) error {
	if _, err := os.Lstat(dstPath); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o750); err != nil {
		return err
	}
	if err := os.Link(srcPath, dstPath); err == nil {
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
