package privategit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/gitexec"
)

func tryCaptureSourceMetadataOnce(layout sourceRepoLayout, stageGitDir string, attempt int, hook func(worktreeDir string, attempt int)) (bool, error) {
	before, hasLock, err := copyAndDigestCapturedMetadataToStage(layout, stageGitDir)
	if err != nil {
		_ = os.RemoveAll(stageGitDir)
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if hasLock {
		_ = os.RemoveAll(stageGitDir)
		return false, nil
	}
	if hook != nil {
		hook(layout.worktreeDir, attempt)
	}
	after, hasLockAfter, err := snapshotMetadataDigests(layout)
	if err != nil || hasLockAfter || len(before) != len(after) {
		_ = os.RemoveAll(stageGitDir)
		return false, err
	}
	for k, v := range before {
		if after[k] != v {
			_ = os.RemoveAll(stageGitDir)
			return false, nil
		}
	}
	return true, nil
}

func readDirEntryNames(dir string) (map[string]bool, bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, err
	}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		name := e.Name()
		names[name] = true
		switch name {
		case "HEAD.lock", "index.lock", "config.lock", "packed-refs.lock":
			return nil, true, nil
		}
	}
	return names, false, nil
}

func snapshotMetadataDigests(layout sourceRepoLayout) (map[string]string, bool, error) {
	gitNames, hasLock, err := readDirEntryNames(layout.gitDir)
	if err != nil || hasLock {
		return nil, hasLock, err
	}
	commonNames := gitNames
	if layout.commonDir != layout.gitDir {
		commonNames, hasLock, err = readDirEntryNames(layout.commonDir)
		if err != nil || hasLock {
			return nil, hasLock, err
		}
	}
	digests := make(map[string]string, 8)
	files := []struct {
		key     string
		dir     string
		name    string
		present bool
	}{
		{"HEAD", layout.gitDir, "HEAD", gitNames["HEAD"]},
		{"index", layout.gitDir, "index", gitNames["index"]},
		{"config", layout.commonDir, "config", commonNames["config"]},
		{"packed-refs", layout.commonDir, "packed-refs", commonNames["packed-refs"]},
	}
	var mu sync.Mutex
	var firstErr error
	setErr := func(e error) {
		if e == nil {
			return
		}
		mu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		mu.Unlock()
	}
	setDigest := func(k, v string) {
		mu.Lock()
		digests[k] = v
		mu.Unlock()
	}
	for _, f := range files {
		if !f.present {
			digests[f.key] = "absent"
		}
	}
	var wg sync.WaitGroup
	for _, f := range files {
		if !f.present {
			continue
		}
		wg.Add(1)
		go func(key, dir, name string) {
			defer wg.Done()
			sum, _, hErr := hashFileBytes(filepath.Join(dir, name))
			if hErr != nil {
				if os.IsNotExist(hErr) {
					setDigest(key, "absent")
					return
				}
				setErr(hErr)
				return
			}
			setDigest(key, sum)
		}(f.key, f.dir, f.name)
	}
	if commonNames["refs"] {
		wg.Add(1)
		go func() {
			defer wg.Done()
			refsDir := filepath.Join(layout.commonDir, "refs")
			walkErr := filepath.WalkDir(refsDir, func(path string, d fs.DirEntry, wErr error) error {
				if wErr != nil {
					if os.IsNotExist(wErr) {
						return nil
					}
					return wErr
				}
				if d.IsDir() {
					return nil
				}
				if strings.HasSuffix(d.Name(), ".lock") {
					mu.Lock()
					hasLock = true
					mu.Unlock()
					return fs.SkipAll
				}
				rel, rErr := filepath.Rel(refsDir, path)
				if rErr != nil {
					return rErr
				}
				sum, _, hErr := hashFileBytes(path)
				if hErr != nil {
					if os.IsNotExist(hErr) {
						mu.Lock()
						hasLock = true
						mu.Unlock()
						return fs.SkipAll
					}
					return hErr
				}
				mu.Lock()
				digests["refs/"+filepath.ToSlash(rel)] = sum
				mu.Unlock()
				return nil
			})
			if walkErr != nil && !os.IsNotExist(walkErr) {
				setErr(walkErr)
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, false, firstErr
	}
	return digests, hasLock, nil
}

func sha256HexBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func copyAndDigestCapturedMetadataToStage(layout sourceRepoLayout, stageGitDir string) (map[string]string, bool, error) {
	gitNames, hasLock, err := readDirEntryNames(layout.gitDir)
	if err != nil || hasLock {
		return nil, hasLock, err
	}
	commonNames := gitNames
	if layout.commonDir != layout.gitDir {
		commonNames, hasLock, err = readDirEntryNames(layout.commonDir)
		if err != nil || hasLock {
			return nil, hasLock, err
		}
	}
	for _, sub := range []string{"refs/heads", "refs/tags", "objects/info", "objects/pack", "info", "logs"} {
		if err := os.MkdirAll(filepath.Join(stageGitDir, sub), 0o755); err != nil {
			return nil, false, err
		}
	}
	digests := make(map[string]string, 8)
	var mu sync.Mutex
	var firstErr error
	setErr := func(e error) {
		if e == nil {
			return
		}
		mu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		mu.Unlock()
	}
	setDigest := func(k, v string) {
		mu.Lock()
		digests[k] = v
		mu.Unlock()
	}
	var wg sync.WaitGroup
	wg.Add(4)
	go func() {
		defer wg.Done()
		headBytes, hErr := os.ReadFile(filepath.Join(layout.gitDir, "HEAD"))
		if hErr != nil {
			setErr(hErr)
			return
		}
		setDigest("HEAD", sha256HexBytes(headBytes))
		setErr(os.WriteFile(filepath.Join(stageGitDir, "HEAD"), headBytes, 0o644))
	}()
	go func() {
		defer wg.Done()
		if !gitNames["index"] {
			setDigest("index", "absent")
			return
		}
		idxBytes, iErr := os.ReadFile(filepath.Join(layout.gitDir, "index"))
		if iErr != nil {
			setErr(iErr)
			return
		}
		setDigest("index", sha256HexBytes(idxBytes))
		setErr(os.WriteFile(filepath.Join(stageGitDir, "index"), idxBytes, 0o644))
	}()
	go func() {
		defer wg.Done()
		var cfgBytes []byte
		if commonNames["config"] {
			var cErr error
			cfgBytes, cErr = os.ReadFile(filepath.Join(layout.commonDir, "config"))
			if cErr != nil {
				setErr(cErr)
				return
			}
			setDigest("config", sha256HexBytes(cfgBytes))
		} else {
			setDigest("config", "absent")
		}
		setErr(os.WriteFile(filepath.Join(stageGitDir, "config"), SanitizeGitConfig(cfgBytes), 0o644))
	}()
	go func() {
		defer wg.Done()
		if !commonNames["packed-refs"] {
			setDigest("packed-refs", "absent")
			return
		}
		prBytes, pErr := os.ReadFile(filepath.Join(layout.commonDir, "packed-refs"))
		if pErr != nil {
			setErr(pErr)
			return
		}
		setDigest("packed-refs", sha256HexBytes(prBytes))
		setErr(os.WriteFile(filepath.Join(stageGitDir, "packed-refs"), prBytes, 0o644))
	}()
	if commonNames["refs"] {
		wg.Add(1)
		go func() {
			defer wg.Done()
			refsSrc := filepath.Join(layout.commonDir, "refs")
			refsDst := filepath.Join(stageGitDir, "refs")
			walkErr := filepath.WalkDir(refsSrc, func(path string, d fs.DirEntry, wErr error) error {
				if wErr != nil {
					return wErr
				}
				if d.Type()&fs.ModeSymlink != 0 {
					return ErrUnauthorizedGitTarget
				}
				rel, rErr := filepath.Rel(refsSrc, path)
				if rErr != nil {
					return rErr
				}
				target := filepath.Join(refsDst, rel)
				if d.IsDir() {
					return os.MkdirAll(target, 0o755)
				}
				if strings.HasSuffix(d.Name(), ".lock") {
					mu.Lock()
					hasLock = true
					mu.Unlock()
					return fs.SkipAll
				}
				raw, readErr := os.ReadFile(path)
				if readErr != nil {
					return readErr
				}
				setDigest("refs/"+filepath.ToSlash(rel), sha256HexBytes(raw))
				return os.WriteFile(target, raw, 0o644)
			})
			setErr(walkErr)
		}()
	}
	if commonNames["logs"] || (layout.gitDir != layout.commonDir && gitNames["logs"]) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if commonNames["logs"] {
				if lErr := copyMetadataTree(filepath.Join(layout.commonDir, "logs"), filepath.Join(stageGitDir, "logs")); lErr != nil {
					setErr(lErr)
					return
				}
			}
			if layout.gitDir != layout.commonDir && gitNames["logs"] {
				setErr(copyOptionalStageFile(filepath.Join(layout.gitDir, "logs", "HEAD"), filepath.Join(stageGitDir, "logs", "HEAD")))
			}
		}()
	}
	if commonNames["info"] {
		wg.Add(1)
		go func() {
			defer wg.Done()
			setErr(copyOptionalStageFile(filepath.Join(layout.commonDir, "info", "exclude"), filepath.Join(stageGitDir, "info", "exclude")))
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, false, firstErr
	}
	return digests, hasLock, nil
}

func copyOptionalStageFile(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.WriteFile(dst, raw, 0o644)
}

func copyMetadataTree(srcDir, dstDir string) error {
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return ErrUnauthorizedGitTarget
		}
		rel, rErr := filepath.Rel(srcDir, path)
		if rErr != nil {
			return rErr
		}
		target := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if strings.HasSuffix(d.Name(), ".lock") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, raw, 0o644)
	})
}

func setupPrivateLinkedWorktreeMetadata(privateGitDir, worktreeDir, wtName string) error {
	wtDir := filepath.Join(privateGitDir, "worktrees", wtName)
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		return err
	}
	headBytes, err := os.ReadFile(filepath.Join(privateGitDir, "HEAD"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(wtDir, "HEAD"), headBytes, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(wtDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(wtDir, "gitdir"), []byte(filepath.Join(worktreeDir, ".git")+"\n"), 0o644)
}

func collectCapturedRefAndIndexOIDs(ctx context.Context, stageGitDir string) ([]string, map[string]string, error) {
	refs := make(map[string]string)
	if packed, err := os.ReadFile(filepath.Join(stageGitDir, "packed-refs")); err == nil {
		for _, line := range strings.Split(string(packed), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 && len(fields[0]) == 40 && isHexString(fields[0]) {
				refs[fields[1]] = strings.ToLower(fields[0])
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, err
	}
	refsRoot := filepath.Join(stageGitDir, "refs")
	walkErr := filepath.WalkDir(refsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || strings.HasSuffix(d.Name(), ".lock") {
			return nil
		}
		rel, rErr := filepath.Rel(stageGitDir, path)
		if rErr != nil {
			return rErr
		}
		refName := filepath.ToSlash(rel)
		raw, rErr := os.ReadFile(path)
		if rErr != nil {
			if os.IsNotExist(rErr) {
				return nil
			}
			return rErr
		}
		val := strings.TrimSpace(string(raw))
		if len(val) == 40 && isHexString(val) {
			refs[refName] = strings.ToLower(val)
			return nil
		}
		if strings.HasPrefix(val, "ref:") {
			return nil
		}
		return fmt.Errorf("%w: invalid ref %s", ErrCorruptObjectStore, refName)
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return nil, nil, walkErr
	}
	var oids []string
	for _, oid := range refs {
		oids = append(oids, oid)
	}
	headRaw, err := os.ReadFile(filepath.Join(stageGitDir, "HEAD"))
	if err != nil {
		return nil, nil, err
	}
	headStr := strings.TrimSpace(string(headRaw))
	if strings.HasPrefix(headStr, "ref:") {
		targetRef := strings.TrimSpace(strings.TrimPrefix(headStr, "ref:"))
		if oid, ok := refs[targetRef]; ok {
			oids = append(oids, oid)
		}
	} else if len(headStr) == 40 && isHexString(headStr) {
		oids = append(oids, strings.ToLower(headStr))
	} else {
		return nil, nil, fmt.Errorf("%w: invalid HEAD %q", ErrCorruptObjectStore, headStr)
	}
	idxPath := filepath.Join(stageGitDir, "index")
	if idxBytes, err := os.ReadFile(idxPath); err == nil {
		idxDigest := sha256HexBytes(idxBytes)
		if cached, ok := parsedIndexOIDCache.Load(idxDigest); ok {
			seen := make(map[string]struct{}, len(oids))
			for _, oid := range oids {
				seen[oid] = struct{}{}
			}
			for _, oid := range cached.([]string) {
				if _, exists := seen[oid]; !exists {
					seen[oid] = struct{}{}
					oids = append(oids, oid)
				}
			}
		} else {
			sink := &lsFilesStageSink{seen: make(map[string]struct{})}
			out, code, gErr := gitexec.RunTo(ctx, filepath.Dir(stageGitDir), []string{"--git-dir=" + stageGitDir, "ls-files", "--stage"}, gitexec.Opts{
				Profile:   gitexec.ProfileHermetic,
				IndexFile: idxPath,
			}, sink)
			sink.flush()
			if gErr != nil || code != 0 {
				return nil, nil, fmt.Errorf("%w: invalid git index (exit %d): %v: %s", ErrCorruptObjectStore, code, gErr, strings.TrimSpace(string(out)))
			}
			parsedIndexOIDCache.Store(idxDigest, append([]string(nil), sink.oids...))
			seen := make(map[string]struct{}, len(oids)+len(sink.oids))
			for _, oid := range oids {
				seen[oid] = struct{}{}
			}
			for _, oid := range sink.oids {
				if _, exists := seen[oid]; !exists {
					seen[oid] = struct{}{}
					oids = append(oids, oid)
				}
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("%w: read git index %s: %v", ErrCorruptObjectStore, idxPath, err)
	}
	sort.Strings(oids)
	return oids, refs, nil
}

var parsedIndexOIDCache sync.Map

type lsFilesStageSink struct {
	rem  []byte
	seen map[string]struct{}
	oids []string
}

func (s *lsFilesStageSink) Write(p []byte) (int, error) {
	s.rem = append(s.rem, p...)
	for {
		nl := bytes.IndexByte(s.rem, '\n')
		if nl < 0 {
			break
		}
		s.parseLine(s.rem[:nl])
		s.rem = s.rem[nl+1:]
	}
	return len(p), nil
}

func (s *lsFilesStageSink) flush() {
	if len(s.rem) > 0 {
		s.parseLine(s.rem)
		s.rem = nil
	}
}

func (s *lsFilesStageSink) parseLine(line []byte) {
	fields := bytes.Fields(line)
	if len(fields) >= 2 && string(fields[0]) != "160000" && len(fields[1]) == 40 {
		oid := strings.ToLower(string(fields[1]))
		if isHexString(oid) {
			if _, ok := s.seen[oid]; !ok {
				s.seen[oid] = struct{}{}
				s.oids = append(s.oids, oid)
			}
		}
	}
}
