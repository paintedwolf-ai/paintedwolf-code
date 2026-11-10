package checkpoint

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/gitexec"
)

const (
	maxVisitedGitObjects, maxMetadataObjectBytes, maxLooseBlobBytes          = 65536, 16 << 20, 512 << 20
	pendingPublishFormatVersion, maxPendingPublishRecordBytes, maxPendingRec = 1, 4 << 20, 4096
)

var (
	wrapObjectCopyReaderFn = func(_ string, r io.Reader) io.Reader { return r }
	syncObjectFileFn       = syncFile
	closeObjectFileFn      = func(f *os.File) error { return f.Close() }
	pendingPublishSeq      atomic.Uint64
)

type pendingPublishRecord struct {
	FormatVersion int      `json:"format_version"`
	OIDs          []string `json:"oids"`
}

func pendingPublishDir(commonGitDir string) string {
	return filepath.Join(commonGitDir, "lycaon-pending-publish")
}

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

type candidateGitObjectSource struct {
	storeDir, repoRoot, gitDir, commonGitDir, targetObjectsDir, packReaderDir string
	candidateDirs, packCandidateDirs                                          []string
	targetHasPacks                                                            bool
	targetFanoutDirs, selectedPackedOIDs, incompleteTargetOIDs                map[string]bool
	priorPendingRecords                                                       map[string][]string
	stagedLoose                                                               map[string]string
	stagedLooseOrder                                                          []string
}

func (s *candidateGitObjectSource) hasTargetFanout(oid string) bool {
	return len(oid) >= 2 && (s.targetFanoutDirs == nil || s.targetFanoutDirs[oid[:2]])
}

func openCandidateGitObjectSource(
	ctx context.Context, storeDir, checkpointID, rootID string, objectCheckpoints []string,
	repoRoot, gitDir, commonGitDir, targetObjectsDir string,
) (*candidateGitObjectSource, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, func() {}, err
	}
	src := &candidateGitObjectSource{
		storeDir: strings.TrimSpace(storeDir), repoRoot: repoRoot, gitDir: gitDir,
		commonGitDir: commonGitDir, targetObjectsDir: targetObjectsDir,
		selectedPackedOIDs: make(map[string]bool), incompleteTargetOIDs: make(map[string]bool),
		priorPendingRecords: make(map[string][]string), stagedLoose: make(map[string]string),
	}
	if err := src.reloadPendingPublishOIDs(true); err != nil {
		return nil, func() {}, err
	}
	hasTargetInfo, hasTargetPack := true, true
	if entries, err := os.ReadDir(targetObjectsDir); err == nil {
		hasTargetInfo, hasTargetPack = false, false
		src.targetFanoutDirs = make(map[string]bool, len(entries))
		for _, d := range entries {
			switch name := d.Name(); {
			case len(name) == 2:
				src.targetFanoutDirs[strings.ToLower(name)] = true
			case name == "info":
				hasTargetInfo = true
			case name == "pack":
				hasTargetPack = true
			}
		}
	}
	if hasTargetInfo {
		if err := rejectGitAlternates(targetObjectsDir); err != nil {
			return nil, func() {}, err
		}
	}
	if hasTargetPack {
		if hasPacks, err := inspectCandidatePackDir(filepath.Join(targetObjectsDir, "pack"), false); err == nil {
			src.targetHasPacks = hasPacks
		}
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
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return nil, func() {}, err
			}
			if st.Mode()&os.ModeSymlink != 0 {
				return nil, func() {}, fmt.Errorf("checkpoint: symlink prohibited in retained git objects: %s", cleanCand)
			}
			if !st.IsDir() {
				continue
			}
			if err := rejectGitAlternates(cleanCand); err != nil {
				return nil, func() {}, err
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

func readPendingPublishRecord(path string) ([]string, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("checkpoint: invalid pending publish entry %s", path)
	}
	raw, err := readBoundedFile(path, maxPendingPublishRecordBytes)
	if err != nil {
		return nil, err
	}
	var rec pendingPublishRecord
	if err := decodeStrict(raw, &rec); err != nil || rec.FormatVersion != pendingPublishFormatVersion || len(rec.OIDs) == 0 {
		return nil, fmt.Errorf("checkpoint: invalid pending publish record %s: %w", path, err)
	}
	for _, oid := range rec.OIDs {
		if oid != strings.ToLower(strings.TrimSpace(oid)) || !isHexOID(oid) || isAllZeroOID(oid) {
			return nil, fmt.Errorf("checkpoint: invalid oid %q in pending publish record %s", oid, path)
		}
	}
	return rec.OIDs, nil
}

func (s *candidateGitObjectSource) reloadPendingPublishOIDs(recordPrior bool) error {
	dir := pendingPublishDir(s.commonGitDir)
	st, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return fmt.Errorf("checkpoint: invalid pending publish state at %s", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > maxPendingRec {
		return errors.Join(err, fmt.Errorf("checkpoint: invalid or oversized pending publish dir %s", dir))
	}
	for _, d := range entries {
		name, entryPath := d.Name(), filepath.Join(dir, d.Name())
		if fseffect.IsStaging(dir, name) {
			if est, lErr := os.Lstat(entryPath); lErr != nil && !errors.Is(lErr, os.ErrNotExist) {
				return lErr
			} else if lErr == nil && (est.Mode()&os.ModeSymlink != 0 || !est.Mode().IsRegular()) {
				return fmt.Errorf("checkpoint: invalid pending publish staging entry %s", entryPath)
			}
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			return fmt.Errorf("checkpoint: unexpected pending publish file %s", entryPath)
		}
		oids, rErr := readPendingPublishRecord(entryPath)
		if errors.Is(rErr, os.ErrNotExist) {
			continue
		} else if rErr != nil {
			return rErr
		}
		if recordPrior {
			s.priorPendingRecords[name] = oids
		}
		for _, oid := range oids {
			s.incompleteTargetOIDs[oid] = true
		}
	}
	return nil
}

func (s *candidateGitObjectSource) resolveWave(ctx context.Context, oids []string) (map[string]batchObjectData, error) {
	resolved := make(map[string]batchObjectData, len(oids))
	dstCorrupt := make(map[string]error)
	var afterDstLoose []string
	for _, oid := range oids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.hasTargetFanout(oid) {
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
		objType, body, found, err := s.resolveCandidateLoose(ctx, oid)
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

func (s *candidateGitObjectSource) resolveCandidateLoose(ctx context.Context, oid string) (string, []byte, bool, error) {
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
			f, openErr := os.Open(srcLoose)
			if openErr != nil {
				return "", nil, true, openErr
			}
			objType, body, vErr := readAndVerifyLooseGitObject(f, oid)
			_ = f.Close()
			if vErr != nil {
				return "", nil, true, vErr
			}
			if _, exists := s.stagedLoose[oid]; !exists {
				s.stagedLooseOrder = append(s.stagedLooseOrder, oid)
			}
			s.stagedLoose[oid] = srcLoose
			return objType, body, true, nil
		}
	}
	return "", nil, false, nil
}

func (s *candidateGitObjectSource) publishVerifiedObjects(ctx context.Context, seen map[string]bool) error {
	dir := pendingPublishDir(s.commonGitDir)
	var ownRecord string
	if len(s.stagedLoose) > 0 || len(s.selectedPackedOIDs) > 0 {
		pubSet := make(map[string]bool, len(s.stagedLoose)+len(s.selectedPackedOIDs))
		for oid := range s.stagedLoose {
			pubSet[oid], s.incompleteTargetOIDs[oid] = true, true
		}
		for oid := range s.selectedPackedOIDs {
			pubSet[oid], s.incompleteTargetOIDs[oid] = true, true
		}
		_, statErr := os.Lstat(dir)
		createdDir := errors.Is(statErr, os.ErrNotExist)
		raw, err := json.Marshal(pendingPublishRecord{FormatVersion: pendingPublishFormatVersion, OIDs: slices.Sorted(maps.Keys(pubSet))})
		if err != nil {
			return err
		}
		var rnd [8]byte
		_, _ = rand.Read(rnd[:])
		ownRecord = fmt.Sprintf("pending-%d-%s-%d.json", os.Getpid(), hex.EncodeToString(rnd[:]), pendingPublishSeq.Add(1))
		if err := writeAtomicJSONOrText(dir, ownRecord, raw, 0o600); err != nil {
			return err
		}
		if createdDir {
			if err := syncDir(s.commonGitDir); err != nil {
				return err
			}
		}
	}
	if err := s.publishSelectedPack(ctx); err != nil {
		return err
	}
	for i := len(s.stagedLooseOrder) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		oid := s.stagedLooseOrder[i]
		dstLoose := filepath.Join(s.targetObjectsDir, oid[:2], oid[2:])
		if _, _, err := copyFileStreamingAtomicWithVerify(s.stagedLoose[oid], dstLoose, 0o444); err != nil {
			return err
		}
		if s.targetFanoutDirs != nil && len(oid) >= 2 {
			s.targetFanoutDirs[oid[:2]] = true
		}
	}
	return s.completePendingPublish(dir, ownRecord, seen)
}

func (s *candidateGitObjectSource) completePendingPublish(dir, ownRecord string, seen map[string]bool) error {
	if ownRecord == "" && len(s.priorPendingRecords) == 0 {
		return nil
	}
	if ownRecord != "" {
		if err := os.Remove(filepath.Join(dir, ownRecord)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for name, oids := range s.priorPendingRecords {
		if slices.ContainsFunc(oids, func(oid string) bool { return !seen[oid] }) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
		if rmErr := os.Remove(dir); rmErr == nil {
			return syncDir(s.commonGitDir)
		}
	}
	return syncDirIfPresent(dir)
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
	altDirs := slices.Clone(s.packCandidateDirs)
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
	if err := os.MkdirAll(dstPackDir, 0o750); err != nil {
		return err
	}
	wroteAny := false
	for _, assetPath := range append(packs, idxs...) {
		if err := ctx.Err(); err != nil {
			return err
		}
		dstPath := filepath.Join(dstPackDir, filepath.Base(assetPath))
		if s.targetHasPacks && isValidDestinationGitAsset(assetPath, dstPath) {
			continue
		}
		if _, _, err := copyFileStreamingAtomicOpts(assetPath, dstPath, 0o444, false, false); err != nil {
			return err
		}
		wroteAny = true
	}
	if wroteAny {
		if err := syncDir(dstPackDir); err != nil {
			return err
		}
	}
	s.targetHasPacks = true
	return nil
}

func copyFileStreamingAtomic(srcPath, dstPath string, mode os.FileMode) error {
	_, _, err := copyFileStreamingAtomicOpts(srcPath, dstPath, mode, true, true)
	return err
}

func copyFileStreamingAtomicWithVerify(srcPath, dstPath string, mode os.FileMode) (string, []byte, error) {
	return copyFileStreamingAtomicOpts(srcPath, dstPath, mode, true, true)
}

func copyFileStreamingAtomicOpts(srcPath, dstPath string, mode os.FileMode, ensureDir, syncParent bool) (string, []byte, error) {
	if ensureDir {
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o750); err != nil {
			return "", nil, err
		}
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
	tmpPath, published := tmpFile.Name(), false
	defer func() {
		_ = tmpFile.Close()
		if !published {
			_ = os.Remove(tmpPath)
		}
	}()
	wrappedIn := wrapObjectCopyReaderFn(srcPath, in)
	var objType string
	var body []byte
	if oid, isLoose := looseObjectOIDForPath(dstPath); isLoose {
		if objType, body, err = readAndVerifyLooseGitObject(io.TeeReader(wrappedIn, tmpFile), oid); err != nil {
			return "", nil, err
		}
		if extra, cErr := io.Copy(tmpFile, wrappedIn); cErr != nil || extra != 0 {
			return "", nil, fmt.Errorf("checkpoint: trailing bytes or copy error for loose object %s: %w", oid, cErr)
		}
	} else {
		srcSHA, sErr := fileDigestSHA256(srcPath)
		if sErr != nil {
			return "", nil, sErr
		}
		dstHash := sha256.New()
		written, cErr := io.Copy(io.MultiWriter(tmpFile, dstHash), wrappedIn)
		if cErr != nil || written == 0 || srcSHA == "" || srcSHA != hex.EncodeToString(dstHash.Sum(nil)) {
			return "", nil, fmt.Errorf("checkpoint: staged git asset copy/digest failure for %s: %w", dstPath, cErr)
		}
	}
	if err := errors.Join(tmpFile.Chmod(mode), syncObjectFileFn(tmpFile), closeObjectFileFn(tmpFile)); err != nil {
		return "", nil, err
	}
	if err := fseffect.Rename(filepath.Dir(dstPath), filepath.Base(tmpPath), filepath.Base(dstPath)); err != nil {
		return "", nil, err
	}
	published = true
	if syncParent {
		return objType, body, syncDir(filepath.Dir(dstPath))
	}
	return objType, body, nil
}

