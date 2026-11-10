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
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
)

type batchObjectMeta struct {
	objType string
	size    int64
}

type batchObjectData struct {
	objType string
	size    int64
	body    []byte
}

type treeEntryKey struct {
	mode string
	name string
}

type parsedTreeEntry struct {
	mode string
	name string
	oid  string
}

type establishedTargetFrontier struct {
	commits map[string]string
	trees   map[string]map[treeEntryKey]string
}

func verifyAndPublishReachableGitGraph(ctx context.Context, source *candidateGitObjectSource, tipOIDs []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	frontier := buildEstablishedTargetFrontier(ctx, source, tipOIDs)
	seen := make(map[string]bool, len(tipOIDs)*4)
	pairedBaseTree := make(map[string]string)
	frontierVerifyOIDs := make(map[string]bool)

	wave := make([]string, 0, len(tipOIDs))
	for _, raw := range tipOIDs {
		oid := strings.ToLower(strings.TrimSpace(raw))
		if !isHexOID(oid) || isAllZeroOID(oid) {
			return fmt.Errorf("checkpoint: invalid git object id %q", raw)
		}
		wave = append(wave, oid)
	}

	for len(wave) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		var pending []string
		inPending := make(map[string]bool, len(wave))
		for _, oid := range wave {
			if seen[oid] || inPending[oid] {
				continue
			}
			if frontier.commits[oid] != "" {
				seen[oid] = true
				continue
			}
			inPending[oid] = true
			pending = append(pending, oid)
		}
		if len(pending) == 0 {
			break
		}
		if len(seen)+len(pending) > maxVisitedGitObjects {
			return fmt.Errorf("checkpoint: git object traversal exceeded %d objects", maxVisitedGitObjects)
		}
		resolved, err := source.resolveWave(ctx, pending)
		if err != nil {
			return err
		}
		var nextWave []string
		for _, oid := range pending {
			data := resolved[oid]
			seen[oid] = true
			switch data.objType {
			case "blob":
			case "commit", "tag":
				primaryOID, parents, pErr := parseCommitOrTagTargets(data.body, data.objType == "commit")
				if pErr != nil {
					return pErr
				}
				if data.objType == "commit" {
					for _, parentOID := range parents {
						if baseTree := frontier.commits[parentOID]; baseTree != "" && pairedBaseTree[primaryOID] == "" {
							pairedBaseTree[primaryOID] = baseTree
						}
					}
				}
				nextWave = append(nextWave, primaryOID)
				for _, parentOID := range parents {
					if frontier.commits[parentOID] != "" {
						seen[parentOID] = true
						continue
					}
					nextWave = append(nextWave, parentOID)
				}
			case "tree":
				entries, tErr := parseTreeTargets(data.body, len(oid)/2)
				if tErr != nil {
					return tErr
				}
				var baseEntries map[treeEntryKey]string
				if baseTreeOID := pairedBaseTree[oid]; baseTreeOID != "" {
					baseEntries = frontier.loadEstablishedTree(ctx, source, baseTreeOID)
				}
				for _, entry := range entries {
					if entry.mode == "160000" {
						continue
					}
					if baseEntries != nil {
						if baseEntryOID, ok := baseEntries[treeEntryKey{mode: entry.mode, name: entry.name}]; ok {
							if entry.oid == baseEntryOID {
								frontierVerifyOIDs[entry.oid] = true
								continue
							}
							if entry.mode == "40000" && pairedBaseTree[entry.oid] == "" {
								pairedBaseTree[entry.oid] = baseEntryOID
							}
						}
					}
					nextWave = append(nextWave, entry.oid)
				}
			default:
				return fmt.Errorf("checkpoint: unsupported git object type %q", data.objType)
			}
		}
		wave = nextWave
	}

	if err := verifyDestinationFrontierBatch(ctx, source, frontierVerifyOIDs, seen); err != nil {
		return err
	}
	return source.publishSelectedPack(ctx)
}

func buildEstablishedTargetFrontier(ctx context.Context, source *candidateGitObjectSource, excludeTipOIDs []string) *establishedTargetFrontier {
	f := &establishedTargetFrontier{
		commits: make(map[string]string),
		trees:   make(map[string]map[treeEntryKey]string),
	}
	excluded := make(map[string]bool, len(excludeTipOIDs))
	for _, oid := range excludeTipOIDs {
		excluded[strings.ToLower(strings.TrimSpace(oid))] = true
	}
	tips := collectDestinationBaseRefTips(source.commonGitDir, source.gitDir, excluded)
	if len(tips) == 0 {
		return f
	}
	resolved, err := readDestinationObjectsOnly(ctx, source, tips)
	if err != nil {
		return f
	}
	var rootTrees []string
	for _, oid := range tips {
		data, ok := resolved[oid]
		if !ok || data.objType != "commit" {
			continue
		}
		treeOID, _, pErr := parseCommitOrTagTargets(data.body, true)
		if pErr == nil && !excluded[oid] {
			f.commits[oid] = treeOID
			rootTrees = append(rootTrees, treeOID)
		}
	}
	for _, treeOID := range rootTrees {
		_ = f.loadEstablishedTree(ctx, source, treeOID)
	}
	return f
}

func (f *establishedTargetFrontier) loadEstablishedTree(ctx context.Context, source *candidateGitObjectSource, treeOID string) map[treeEntryKey]string {
	if existing, ok := f.trees[treeOID]; ok {
		return existing
	}
	resolved, err := readDestinationObjectsOnly(ctx, source, []string{treeOID})
	if err != nil {
		return nil
	}
	data, ok := resolved[treeOID]
	if !ok || data.objType != "tree" {
		return nil
	}
	entries, err := parseTreeTargets(data.body, len(treeOID)/2)
	if err != nil {
		return nil
	}
	m := make(map[treeEntryKey]string, len(entries))
	for _, e := range entries {
		m[treeEntryKey{mode: e.mode, name: e.name}] = e.oid
	}
	f.trees[treeOID] = m
	return m
}

func readDestinationObjectsOnly(ctx context.Context, source *candidateGitObjectSource, oids []string) (map[string]batchObjectData, error) {
	resolved := make(map[string]batchObjectData, len(oids))
	var packed []string
	for _, oid := range oids {
		dstLoose := filepath.Join(source.targetObjectsDir, oid[:2], oid[2:])
		if f, err := os.Open(dstLoose); err == nil {
			objType, body, vErr := readAndVerifyLooseGitObject(f, oid)
			_ = f.Close()
			if vErr == nil {
				resolved[oid] = batchObjectData{objType: objType, body: body}
				continue
			}
		}
		packed = append(packed, oid)
	}
	if len(packed) > 0 && source.targetHasPacks {
		_ = resolvePackedBatchInto(ctx, source.repoRoot, source.commonGitDir, packed, false, resolved, nil)
	}
	return resolved, nil
}

func collectDestinationBaseRefTips(commonGitDir, gitDir string, excluded map[string]bool) []string {
	seen := make(map[string]bool)
	var tips []string
	addOID := func(oid string) {
		oid = strings.ToLower(strings.TrimSpace(oid))
		if isHexOID(oid) && !isAllZeroOID(oid) && !excluded[oid] && !seen[oid] && len(tips) < 64 {
			seen[oid] = true
			tips = append(tips, oid)
		}
	}
	for _, dir := range []string{gitDir, commonGitDir} {
		if raw, err := readBoundedFile(filepath.Join(dir, "HEAD"), 512); err == nil {
			head := strings.TrimSpace(string(raw))
			if strings.HasPrefix(head, "ref:") {
				refName := strings.TrimSpace(strings.TrimPrefix(head, "ref:"))
				if strings.HasPrefix(refName, "refs/heads/") || strings.HasPrefix(refName, "refs/tags/") {
					addOID(resolveExistingGitRef(commonGitDir, gitDir, refName))
				}
			} else {
				addOID(head)
			}
		}
	}
	for _, dir := range []string{commonGitDir, gitDir} {
		if entries, err := os.ReadDir(filepath.Join(dir, "refs", "heads")); err == nil {
			for _, d := range entries {
				if !d.IsDir() && len(tips) < 64 {
					if raw, rErr := readBoundedFile(filepath.Join(dir, "refs", "heads", d.Name()), 256); rErr == nil {
						addOID(string(raw))
					}
				}
			}
		}
		if raw, err := readBoundedFile(filepath.Join(dir, "packed-refs"), 8<<20); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				f := strings.Fields(strings.TrimSpace(line))
				if len(f) >= 2 && strings.HasPrefix(f[1], "refs/heads/") {
					addOID(f[0])
				}
			}
		}
	}
	return tips
}

func verifyDestinationFrontierBatch(ctx context.Context, source *candidateGitObjectSource, frontierOIDs map[string]bool, seen map[string]bool) error {
	if len(frontierOIDs) == 0 {
		return nil
	}
	var needPacked []string
	for _, oid := range slices.Sorted(maps.Keys(frontierOIDs)) {
		if seen[oid] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		dstLoose := filepath.Join(source.targetObjectsDir, oid[:2], oid[2:])
		if f, err := os.Open(dstLoose); err == nil {
			_, _, vErr := readAndVerifyLooseGitObject(f, oid)
			_ = f.Close()
			if vErr == nil {
				seen[oid] = true
				continue
			}
		}
		needPacked = append(needPacked, oid)
	}
	if len(needPacked) == 0 {
		return nil
	}
	var missing []string
	if source.targetHasPacks {
		meta, err := batchCheckObjects(ctx, source.repoRoot, source.commonGitDir, needPacked)
		if err != nil {
			return err
		}
		for _, oid := range needPacked {
			if _, ok := meta[oid]; ok {
				seen[oid] = true
			} else {
				missing = append(missing, oid)
			}
		}
	} else {
		missing = needPacked
	}
	if len(missing) > 0 {
		_, err := source.resolveWave(ctx, missing)
		return err
	}
	return nil
}

func resolvePackedBatchInto(
	ctx context.Context, repoRoot, gitDir string, oids []string, verifyPackedBlobs bool,
	resolved map[string]batchObjectData, selectedPacked map[string]bool,
) error {
	metaMap, err := batchCheckObjects(ctx, repoRoot, gitDir, oids)
	if err != nil || len(metaMap) == 0 {
		return err
	}
	var metaOIDs []string
	for _, oid := range oids {
		m, ok := metaMap[oid]
		if !ok {
			continue
		}
		if m.objType == "blob" {
			if m.size < 0 || m.size > maxLooseBlobBytes {
				return fmt.Errorf("size or limit mismatch for packed blob %s", oid)
			}
			if verifyPackedBlobs {
				if _, _, bErr := readAndVerifyPackedGitObject(ctx, repoRoot, gitDir, oid); bErr != nil {
					return bErr
				}
			}
			resolved[oid] = batchObjectData{objType: "blob", size: m.size}
			if selectedPacked != nil {
				selectedPacked[oid] = true
			}
			continue
		}
		if m.size < 0 || m.size > maxMetadataObjectBytes {
			return fmt.Errorf("size or limit mismatch for packed object %s", oid)
		}
		metaOIDs = append(metaOIDs, oid)
	}
	if len(metaOIDs) == 0 {
		return nil
	}
	dataMap, err := batchReadMetadataObjects(ctx, repoRoot, gitDir, metaOIDs)
	if err != nil {
		return err
	}
	for _, oid := range metaOIDs {
		d, ok := dataMap[oid]
		if !ok {
			return fmt.Errorf("corrupt packed object %s", oid)
		}
		resolved[oid] = d
		if selectedPacked != nil {
			selectedPacked[oid] = true
		}
	}
	return nil
}

func batchCheckObjects(ctx context.Context, repoRoot, gitDir string, oids []string) (map[string]batchObjectMeta, error) {
	res := make(map[string]batchObjectMeta, len(oids))
	const chunkSize = 2048
	for start := 0; start < len(oids); start += chunkSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunk := oids[start:min(start+chunkSize, len(oids))]
		input := []byte(strings.Join(chunk, "\n") + "\n")
		out, code, err := gitexec.Run(ctx, repoRoot, []string{"--git-dir=" + gitDir, "cat-file", "--batch-check"}, gitexec.Opts{
			Profile:   gitexec.ProfileHermetic,
			Input:     input,
			MaxOutput: 16 << 20,
		})
		if err != nil || code != 0 {
			return nil, fmt.Errorf("git cat-file --batch-check: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(strings.TrimSpace(line))
			if len(f) != 3 || f[1] == "missing" {
				continue
			}
			sz, sErr := strconv.ParseInt(f[2], 10, 64)
			if sErr == nil {
				res[strings.ToLower(f[0])] = batchObjectMeta{objType: f[1], size: sz}
			}
		}
	}
	return res, nil
}

func batchReadMetadataObjects(ctx context.Context, repoRoot, gitDir string, oids []string) (map[string]batchObjectData, error) {
	res := make(map[string]batchObjectData, len(oids))
	const chunkSize = 128
	for start := 0; start < len(oids); start += chunkSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunk := oids[start:min(start+chunkSize, len(oids))]
		input := []byte(strings.Join(chunk, "\n") + "\n")
		out, code, err := gitexec.Run(ctx, repoRoot, []string{"--git-dir=" + gitDir, "cat-file", "--batch"}, gitexec.Opts{
			Profile:   gitexec.ProfileHermetic,
			Input:     input,
			MaxOutput: 64 << 20,
		})
		if err != nil || code != 0 {
			return nil, fmt.Errorf("git cat-file --batch: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		for len(out) > 0 {
			nl := bytes.IndexByte(out, '\n')
			if nl < 0 {
				return nil, fmt.Errorf("checkpoint: truncated git cat-file --batch header")
			}
			hdr := strings.TrimSpace(string(out[:nl]))
			out = out[nl+1:]
			if hdr == "" {
				continue
			}
			f := strings.Fields(hdr)
			if len(f) >= 2 && f[1] == "missing" {
				continue
			}
			if len(f) != 3 {
				return nil, fmt.Errorf("checkpoint: malformed git cat-file --batch header %q", hdr)
			}
			oid, objType := strings.ToLower(f[0]), f[1]
			size, sErr := strconv.ParseInt(f[2], 10, 64)
			if sErr != nil || size < 0 || size > maxMetadataObjectBytes || int64(len(out)) < size+1 || out[size] != '\n' {
				return nil, fmt.Errorf("checkpoint: invalid git cat-file --batch payload for %s", oid)
			}
			res[oid] = batchObjectData{objType: objType, size: size, body: bytes.Clone(out[:size])}
			out = out[size+1:]
		}
	}
	return res, nil
}

func readAndVerifyLooseGitObject(f *os.File, oid string) (string, []byte, error) {
	zr, err := zlib.NewReader(f)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = zr.Close() }()
	h := sha1.New() // #nosec G401 -- Verify standard Git object identifiers, not security signatures.
	if len(oid) == 64 {
		h = sha256.New()
	}
	tr := io.TeeReader(zr, h)
	var hdrBuf [64]byte
	hdrLen, foundNul := 0, false
	for hdrLen < len(hdrBuf) {
		var b [1]byte
		if _, rErr := io.ReadFull(tr, b[:]); rErr != nil {
			return "", nil, fmt.Errorf("malformed loose object header for %s: %w", oid, rErr)
		}
		if b[0] == 0 {
			foundNul = true
			break
		}
		hdrBuf[hdrLen] = b[0]
		hdrLen++
	}
	hdrType, hdrSize, ok := strings.Cut(string(hdrBuf[:hdrLen]), " ")
	size, sErr := strconv.ParseInt(hdrSize, 10, 64)
	if !foundNul || !ok || sErr != nil || size < 0 {
		return "", nil, fmt.Errorf("malformed loose object header for %s", oid)
	}
	var body []byte
	if hdrType == "blob" {
		if n, cErr := io.Copy(io.Discard, io.LimitReader(tr, size+1)); size > maxLooseBlobBytes || cErr != nil || n != size {
			return "", nil, fmt.Errorf("size or limit mismatch for loose blob %s", oid)
		}
	} else {
		var rErr error
		if body, rErr = io.ReadAll(io.LimitReader(tr, size+1)); size > maxMetadataObjectBytes || rErr != nil || int64(len(body)) != size {
			return "", nil, fmt.Errorf("size or limit mismatch for loose object %s", oid)
		}
	}
	if hex.EncodeToString(h.Sum(nil)) != oid {
		return "", nil, fmt.Errorf("digest mismatch for loose object %s", oid)
	}
	return hdrType, body, nil
}

func readAndVerifyPackedGitObject(ctx context.Context, repoRoot, commonGitDir, oid string) (string, []byte, error) {
	typeOut, code, err := gitexec.Run(ctx, repoRoot, []string{"--git-dir=" + commonGitDir, "cat-file", "-t", oid}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	if err != nil || code != 0 {
		return "", nil, fmt.Errorf("missing git object %s: %w (%s)", oid, err, strings.TrimSpace(string(typeOut)))
	}
	objType := strings.TrimSpace(string(typeOut))
	if objType == "blob" {
		diag, bCode, bErr := gitexec.RunTo(ctx, repoRoot, []string{"--git-dir=" + commonGitDir, "cat-file", "blob", oid}, gitexec.Opts{Profile: gitexec.ProfileHermetic}, io.Discard)
		if bErr != nil || bCode != 0 {
			return "", nil, fmt.Errorf("corrupt packed blob %s: %w (%s)", oid, bErr, strings.TrimSpace(string(diag)))
		}
		return "blob", nil, nil
	}
	body, cCode, cErr := gitexec.Run(ctx, repoRoot, []string{"--git-dir=" + commonGitDir, "cat-file", objType, oid}, gitexec.Opts{Profile: gitexec.ProfileHermetic, MaxOutput: maxMetadataObjectBytes})
	if cErr != nil || cCode != 0 {
		return "", nil, fmt.Errorf("corrupt packed %s %s: %w", objType, oid, cErr)
	}
	return objType, body, nil
}

func parseCommitOrTagTargets(body []byte, isCommit bool) (string, []string, error) {
	var primaryOID string
	var parents []string
	for _, line := range strings.Split(string(body), "\n") {
		if line == "" {
			break
		}
		f := strings.Fields(line)
		if len(f) == 0 || !((isCommit && (f[0] == "tree" || f[0] == "parent")) || (!isCommit && f[0] == "object")) {
			continue
		}
		target := strings.ToLower(strings.TrimSpace(f[min(1, len(f)-1)]))
		if len(f) != 2 || !isHexOID(target) || isAllZeroOID(target) {
			return "", nil, fmt.Errorf("checkpoint: invalid %s target oid %q", f[0], line)
		}
		if f[0] == "tree" || f[0] == "object" {
			primaryOID = target
		} else {
			parents = append(parents, target)
		}
	}
	if primaryOID == "" {
		return "", nil, fmt.Errorf("checkpoint: missing required target object header")
	}
	return primaryOID, parents, nil
}

func parseTreeTargets(body []byte, hashBytes int) ([]parsedTreeEntry, error) {
	var entries []parsedTreeEntry
	for len(body) > 0 {
		n := bytes.IndexByte(body, 0)
		if n <= 0 || n+1+hashBytes > len(body) {
			return nil, fmt.Errorf("checkpoint: malformed tree entry")
		}
		hdr := string(body[:n])
		mode, name, ok := strings.Cut(hdr, " ")
		if !ok || mode == "" || name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
			return nil, fmt.Errorf("checkpoint: invalid tree entry header %q", hdr)
		}
		entryOID := hex.EncodeToString(body[n+1 : n+1+hashBytes])
		body = body[n+1+hashBytes:]
		if !isHexOID(entryOID) || isAllZeroOID(entryOID) {
			return nil, fmt.Errorf("checkpoint: invalid tree entry oid %q", entryOID)
		}
		entries = append(entries, parsedTreeEntry{mode: mode, name: name, oid: entryOID})
	}
	return entries, nil
}
