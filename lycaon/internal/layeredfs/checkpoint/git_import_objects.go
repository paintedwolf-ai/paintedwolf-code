package checkpoint

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1" // #nosec G505 -- SHA-1 is the object identifier in the admitted Git repository format.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/gitexec"
)

const (
	maxVisitedGitObjects, maxMetadataObjectBytes, maxLooseBlobBytes = 65536, 16 << 20, 512 << 20
)

var (
	wrapObjectCopyReaderFn = func(_ string, r io.Reader) io.Reader { return r }
	syncObjectFileFn       = syncFile
	closeObjectFileFn      = func(f *os.File) error { return f.Close() }
)

// WriteLooseGitObject writes a standard zlib-compressed loose Git object into objectsDir and returns its SHA-1 hex OID.
func WriteLooseGitObject(objectsDir, objType string, payload []byte) (string, error) {
	raw := append([]byte(fmt.Sprintf("%s %d\x00", objType, len(payload))), payload...)
	oid := hexObjSHA1(raw)
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	_, wErr := zw.Write(raw)
	if err := errors.Join(wErr, zw.Close()); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(objectsDir, oid[:2]), 0o750); err != nil {
		return "", err
	}
	return oid, writeAtomicJSONOrText(filepath.Join(objectsDir, oid[:2]), oid[2:], compressed.Bytes(), 0o444)
}

func ensureMinimalGitDirLayout(gitDir, commonGitDir string) error {
	for _, d := range []string{filepath.Join(commonGitDir, "objects"), filepath.Join(commonGitDir, "refs")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(filepath.Join(commonGitDir, "config")); errors.Is(err, os.ErrNotExist) {
		if wErr := writeAtomicJSONOrText(commonGitDir, "config", []byte("[core]\n\trepositoryformatversion = 0\n"), 0o644); wErr != nil {
			return wErr
		}
	}
	for _, d := range []string{commonGitDir, gitDir} {
		if _, err := os.Lstat(filepath.Join(d, "HEAD")); errors.Is(err, os.ErrNotExist) {
			if wErr := writeAtomicJSONOrText(d, "HEAD", []byte("ref: refs/heads/main\n"), 0o644); wErr != nil {
				return wErr
			}
		}
	}
	return nil
}

type candidateGitObjectSource struct {
	storeDir           string
	repoRoot           string
	gitDir             string
	commonGitDir       string
	targetObjectsDir   string
	candidateDirs      []string
	packCandidateDirs  []string
	targetHasPacks     bool
	packReaderDir      string
	selectedPackedOIDs map[string]bool
}

func openCandidateGitObjectSource(
	ctx context.Context, storeDir, checkpointID, rootID string, objectCheckpoints []string,
	repoRoot, gitDir, commonGitDir, targetObjectsDir string,
) (*candidateGitObjectSource, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, func() {}, err
	}
	src := &candidateGitObjectSource{
		storeDir:           strings.TrimSpace(storeDir),
		repoRoot:           repoRoot,
		gitDir:             gitDir,
		commonGitDir:       commonGitDir,
		targetObjectsDir:   targetObjectsDir,
		selectedPackedOIDs: make(map[string]bool),
	}
	if hasPacks, err := inspectCandidatePackDir(filepath.Join(targetObjectsDir, "pack"), false); err == nil {
		src.targetHasPacks = hasPacks
	}
	if src.storeDir != "" {
		parent2 := filepath.Dir(filepath.Dir(src.storeDir))
		var rawCandidates []string
		for _, id := range append([]string{checkpointID}, objectCheckpoints...) {
			if id != "" {
				rawCandidates = append(rawCandidates, CheckpointGitObjectsDir(src.storeDir, id, rootID))
			}
		}
		rawCandidates = append(rawCandidates,
			filepath.Join(src.storeDir, "objects"),
			filepath.Join(parent2, "privategit", "retained-store", "objects"),
			filepath.Join(parent2, "worker-branches", ".adapter", "privategit", "retained-store", "objects"),
		)
		seen := make(map[string]bool, len(rawCandidates))
		cleanTarget := filepath.Clean(targetObjectsDir)
		for _, cand := range rawCandidates {
			cleanCand := filepath.Clean(cand)
			if seen[cleanCand] || cleanCand == cleanTarget {
				continue
			}
			st, err := os.Lstat(cleanCand)
			if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
				continue
			}
			hasPacks, pErr := inspectCandidatePackDir(filepath.Join(cleanCand, "pack"), true)
			if pErr != nil {
				return nil, func() {}, pErr
			}
			seen[cleanCand] = true
			src.candidateDirs = append(src.candidateDirs, cleanCand)
			if hasPacks {
				src.packCandidateDirs = append(src.packCandidateDirs, cleanCand)
			}
		}
	}
	cleanup := func() {
		if src.packReaderDir != "" {
			_ = os.RemoveAll(src.packReaderDir)
		}
	}
	return src, cleanup, nil
}

func inspectCandidatePackDir(packDir string, rejectSymlinks bool) (bool, error) {
	st, err := os.Lstat(packDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		if rejectSymlinks {
			return false, fmt.Errorf("checkpoint: symlink prohibited in retained git objects: %s", packDir)
		}
		return false, nil
	}
	if !st.IsDir() {
		return false, nil
	}
	entries, err := os.ReadDir(packDir)
	if err != nil {
		return false, err
	}
	hasPack := false
	for _, d := range entries {
		if d.Type()&os.ModeSymlink != 0 {
			if rejectSymlinks {
				return false, fmt.Errorf("checkpoint: symlink prohibited in retained git objects: %s", filepath.Join(packDir, d.Name()))
			}
			continue
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".pack") {
			hasPack = true
		}
	}
	return hasPack, nil
}

func (s *candidateGitObjectSource) resolveWave(ctx context.Context, oids []string) (map[string]batchObjectData, error) {
	resolved := make(map[string]batchObjectData, len(oids))
	dstCorrupt := make(map[string]error)
	var afterDstLoose []string
	for _, oid := range oids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dstLoose := filepath.Join(s.targetObjectsDir, oid[:2], oid[2:])
		if f, openErr := os.Open(dstLoose); openErr == nil {
			objType, body, vErr := readAndVerifyLooseGitObject(f, oid)
			_ = f.Close()
			if vErr == nil {
				resolved[oid] = batchObjectData{objType: objType, body: body}
				continue
			}
			dstCorrupt[oid] = vErr
		} else if !errors.Is(openErr, os.ErrNotExist) {
			return nil, openErr
		}
		afterDstLoose = append(afterDstLoose, oid)
	}
	if len(afterDstLoose) > 0 && s.targetHasPacks {
		var dstPackCandidates []string
		for _, oid := range afterDstLoose {
			if dstCorrupt[oid] == nil {
				dstPackCandidates = append(dstPackCandidates, oid)
			}
		}
		if err := resolvePackedBatchInto(ctx, s.repoRoot, s.commonGitDir, dstPackCandidates, true, resolved, nil); err != nil {
			return nil, err
		}
	}
	var afterDst []string
	for _, oid := range afterDstLoose {
		if _, ok := resolved[oid]; !ok {
			afterDst = append(afterDst, oid)
		}
	}
	if len(afterDst) > 0 && len(s.packCandidateDirs) > 0 {
		readerDir, err := s.ensurePackReaderDir()
		if err != nil {
			return nil, err
		}
		if err := resolvePackedBatchInto(ctx, readerDir, readerDir, afterDst, false, resolved, s.selectedPackedOIDs); err != nil {
			return nil, err
		}
	}
	for _, oid := range afterDst {
		if _, ok := resolved[oid]; ok {
			continue
		}
		objType, body, found, err := s.resolveAndPublishCandidateLoose(ctx, oid)
		if err != nil {
			return nil, err
		}
		if found {
			resolved[oid] = batchObjectData{objType: objType, body: body}
			continue
		}
		if dstCorrupt[oid] != nil {
			return nil, dstCorrupt[oid]
		}
		if s.targetHasPacks {
			_, _, pErr := readAndVerifyPackedGitObject(ctx, s.repoRoot, s.commonGitDir, oid)
			return nil, pErr
		}
		return nil, fmt.Errorf("missing git object %s: %w", oid, os.ErrNotExist)
	}
	return resolved, nil
}

func (s *candidateGitObjectSource) resolveAndPublishCandidateLoose(ctx context.Context, oid string) (string, []byte, bool, error) {
	dstLoose := filepath.Join(s.targetObjectsDir, oid[:2], oid[2:])
	for _, candDir := range s.candidateDirs {
		if err := ctx.Err(); err != nil {
			return "", nil, false, err
		}
		fanoutDir := filepath.Join(candDir, oid[:2])
		if fst, fErr := os.Lstat(fanoutDir); fErr == nil && fst.Mode()&os.ModeSymlink != 0 {
			return "", nil, false, fmt.Errorf("checkpoint: symlink prohibited in retained git objects: %s", fanoutDir)
		}
		srcLoose := filepath.Join(fanoutDir, oid[2:])
		st, sErr := os.Lstat(srcLoose)
		if sErr != nil {
			if errors.Is(sErr, os.ErrNotExist) {
				continue
			}
			return "", nil, false, sErr
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return "", nil, false, fmt.Errorf("checkpoint: symlink prohibited in retained git objects: %s", srcLoose)
		}
		if st.Mode().IsRegular() {
			objType, body, err := copyFileStreamingAtomicWithVerify(srcLoose, dstLoose, 0o444)
			return objType, body, true, err
		}
	}
	return "", nil, false, nil
}

func (s *candidateGitObjectSource) ensurePackReaderDir() (string, error) {
	if s.packReaderDir != "" {
		return s.packReaderDir, nil
	}
	tmpDir, err := os.MkdirTemp("", "lycaon-cp-pack-*")
	if err != nil {
		return "", err
	}
	if err := ensureMinimalGitDirLayout(tmpDir, tmpDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", err
	}
	infoDir := filepath.Join(tmpDir, "objects", "info")
	if err := os.MkdirAll(infoDir, 0o750); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", err
	}
	altDirs := append(slices.Clone(s.packCandidateDirs), s.targetObjectsDir)
	if err := os.WriteFile(filepath.Join(infoDir, "alternates"), []byte(strings.Join(altDirs, "\n")+"\n"), 0o600); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", err
	}
	s.packReaderDir = tmpDir
	return tmpDir, nil
}

func (s *candidateGitObjectSource) publishSelectedPack(ctx context.Context) error {
	if len(s.selectedPackedOIDs) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	readerDir, err := s.ensurePackReaderDir()
	if err != nil {
		return err
	}
	selected := slices.Sorted(maps.Keys(s.selectedPackedOIDs))
	stagedPackDir := filepath.Join(readerDir, "staged-pack")
	if err := os.MkdirAll(stagedPackDir, 0o750); err != nil {
		return err
	}
	input := []byte(strings.Join(selected, "\n") + "\n")
	out, code, err := gitexec.Run(ctx, readerDir, []string{"--git-dir=" + readerDir, "pack-objects", "--quiet", filepath.Join(stagedPackDir, "pack")}, gitexec.Opts{
		Profile: gitexec.ProfileHermetic,
		Input:   input,
	})
	if err != nil || code != 0 {
		return fmt.Errorf("checkpoint: pack selected git objects (exit %d): %w (%s)", code, err, strings.TrimSpace(string(out)))
	}
	entries, err := os.ReadDir(stagedPackDir)
	if err != nil {
		return err
	}
	var packs, idxs []string
	for _, d := range entries {
		if d.IsDir() {
			continue
		}
		switch filepath.Ext(d.Name()) {
		case ".pack":
			packs = append(packs, filepath.Join(stagedPackDir, d.Name()))
		case ".idx":
			idxs = append(idxs, filepath.Join(stagedPackDir, d.Name()))
		}
	}
	if len(packs) == 0 || len(packs) != len(idxs) {
		return fmt.Errorf("checkpoint: incomplete staged selected pack in %s", stagedPackDir)
	}
	for _, packPath := range packs {
		vOut, vCode, vErr := gitexec.Run(ctx, readerDir, []string{"--git-dir=" + readerDir, "verify-pack", packPath}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
		if vErr != nil || vCode != 0 {
			return fmt.Errorf("checkpoint: corrupt selected pack %s: %w (%s)", filepath.Base(packPath), vErr, strings.TrimSpace(string(vOut)))
		}
	}
	dstPackDir := filepath.Join(s.targetObjectsDir, "pack")
	for _, assetPath := range append(packs, idxs...) {
		if err := ctx.Err(); err != nil {
			return err
		}
		dstPath := filepath.Join(dstPackDir, filepath.Base(assetPath))
		if isValidDestinationGitAsset(assetPath, dstPath) {
			continue
		}
		if err := copyFileStreamingAtomic(assetPath, dstPath, 0o444); err != nil {
			return err
		}
	}
	s.targetHasPacks = true
	return nil
}

func copyFileStreamingAtomic(srcPath, dstPath string, mode os.FileMode) error {
	_, _, err := copyFileStreamingAtomicWithVerify(srcPath, dstPath, mode)
	return err
}

func copyFileStreamingAtomicWithVerify(srcPath, dstPath string, mode os.FileMode) (string, []byte, error) {
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o750); err != nil {
		return "", nil, err
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = in.Close() }()
	tmpFile, err := os.CreateTemp(filepath.Dir(dstPath), "."+filepath.Base(dstPath)+".tmp-*")
	if err != nil {
		return "", nil, err
	}
	tmpPath := tmpFile.Name()
	published := false
	defer func() {
		_ = tmpFile.Close()
		if !published {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := io.Copy(tmpFile, wrapObjectCopyReaderFn(srcPath, in)); err != nil {
		return "", nil, err
	}
	if err := tmpFile.Chmod(mode); err != nil {
		return "", nil, err
	}
	if err := errors.Join(syncObjectFileFn(tmpFile), closeObjectFileFn(tmpFile)); err != nil {
		return "", nil, err
	}
	objType, body, err := publishStagedGitAssetWithVerify(srcPath, tmpPath, dstPath)
	if err != nil {
		return "", nil, err
	}
	published = true
	return objType, body, syncDir(filepath.Dir(dstPath))
}

func publishStagedGitAsset(srcPath, tmpPath, dstPath string) error {
	_, _, err := publishStagedGitAssetWithVerify(srcPath, tmpPath, dstPath)
	return err
}

func publishStagedGitAssetWithVerify(srcPath, tmpPath, dstPath string) (string, []byte, error) {
	var (
		objType string
		body    []byte
	)
	if oid, ok := looseObjectOIDForPath(dstPath); ok {
		f, err := os.Open(tmpPath)
		if err != nil {
			return "", nil, err
		}
		var vErr error
		objType, body, vErr = readAndVerifyLooseGitObject(f, oid)
		_ = f.Close()
		if vErr != nil {
			return "", nil, vErr
		}
	} else if !isValidDestinationGitAsset(srcPath, tmpPath) {
		return "", nil, fmt.Errorf("checkpoint: staged git asset digest mismatch for %s", dstPath)
	}
	if linkErr := os.Link(tmpPath, dstPath); linkErr == nil {
		if rmErr := os.Remove(tmpPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return "", nil, rmErr
		}
		return objType, body, nil
	} else if errors.Is(linkErr, os.ErrExist) && isValidDestinationGitAsset(srcPath, dstPath) {
		if rmErr := os.Remove(tmpPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return "", nil, rmErr
		}
		return objType, body, nil
	}
	return objType, body, fseffect.Rename(filepath.Dir(dstPath), filepath.Base(tmpPath), filepath.Base(dstPath))
}

func isValidDestinationGitAsset(srcPath, dstPath string) bool {
	dstSt, err := os.Lstat(dstPath)
	if err != nil || !dstSt.Mode().IsRegular() || dstSt.Size() <= 0 {
		return false
	}
	if oid, ok := looseObjectOIDForPath(dstPath); ok {
		f, openErr := os.Open(dstPath)
		if openErr != nil {
			return false
		}
		_, _, vErr := readAndVerifyLooseGitObject(f, oid)
		_ = f.Close()
		return vErr == nil
	}
	srcSt, err := os.Lstat(srcPath)
	if err != nil || !srcSt.Mode().IsRegular() || srcSt.Size() != dstSt.Size() {
		return false
	}
	srcSHA, sErr := fileDigestSHA256(srcPath)
	dstSHA, dErr := fileDigestSHA256(dstPath)
	return sErr == nil && dErr == nil && srcSHA != "" && srcSHA == dstSHA
}

func looseObjectOIDForPath(p string) (string, bool) {
	prefix, suffix := filepath.Base(filepath.Dir(p)), filepath.Base(p)
	oid := strings.ToLower(prefix + suffix)
	return oid, len(prefix) == 2 && isHexOID(oid) && !isAllZeroOID(oid)
}

func fileDigestSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	_, cErr := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), cErr
}

func readBoundedFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("checkpoint: read %s failed or exceeded limit: %w", path, err)
	}
	return data, nil
}

func hexObjSHA1(b []byte) string {
	s := sha1.Sum(b) /* #nosec G401 -- Standard Git object identifier. */
	return hex.EncodeToString(s[:])
}
func isHexOID(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil && (len(s) == 40 || len(s) == 64)
}
func isAllZeroOID(s string) bool { return strings.Trim(s, "0") == "" }

// CheckpointGitObjectsDir is where a checkpoint keeps the objects its refs add for one root; an empty
// root ID is the primary root.
func CheckpointGitObjectsDir(storeDir, checkpointID, rootID string) string {
	if rootID == "" {
		return filepath.Join(storeDir, "checkpoints", checkpointID, "git-objects")
	}
	return filepath.Join(storeDir, "checkpoints", checkpointID, "git-objects-roots", rootID)
}

func selectPrimaryRootDir(targetRoots map[string]string) string {
	return strings.TrimSpace(targetRoots[selectPrimaryRootID(targetRoots)])
}

func selectPrimaryRootID(targetRoots map[string]string) string {
	for _, k := range []string{"primary", "root-0", ""} {
		if strings.TrimSpace(targetRoots[k]) != "" {
			return k
		}
	}
	for _, id := range slices.Sorted(maps.Keys(targetRoots)) {
		if strings.TrimSpace(targetRoots[id]) != "" {
			return id
		}
	}
	return ""
}
