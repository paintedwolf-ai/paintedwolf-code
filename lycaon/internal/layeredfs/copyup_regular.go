package layeredfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"time"
)

func tryCaptureTrunkOnce(
	c *Core,
	qpath QualifiedPath,
	provider LowerProvider,
	attempt int,
	retryHook func(QualifiedPath, int),
) (BaseCapture, bool, error) {
	c.counters.lowerLookups.Add(1)
	before, err := provider.Lstat(qpath.Rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return BaseCapture{
				Path:       qpath,
				Layer:      LayerTrunk,
				Kind:       EntryAbsent,
				CapturedAt: time.Now(),
			}, true, nil
		}
		return BaseCapture{}, false, err
	}

	switch before.Kind {
	case EntryAbsent:
		return BaseCapture{
			Path:       qpath,
			Layer:      LayerTrunk,
			Kind:       EntryAbsent,
			CapturedAt: time.Now(),
		}, true, nil

	case EntryDirectory:
		xattrs, _ := provider.ListXattr(qpath.Rel)
		if retryHook != nil {
			retryHook(qpath, attempt)
		}
		c.counters.lowerLookups.Add(1)
		after, aErr := provider.Lstat(qpath.Rel)
		if aErr != nil || !sameLowerIdentity(before, after) {
			return BaseCapture{}, false, nil
		}
		return BaseCapture{
			Path:       qpath,
			Layer:      LayerTrunk,
			Kind:       EntryDirectory,
			Mode:       before.Mode,
			ModTime:    before.ModTime,
			Xattrs:     xattrs,
			Ino:        before.Ino,
			CapturedAt: time.Now(),
		}, true, nil

	case EntrySymlink:
		target, lErr := provider.Readlink(qpath.Rel)
		if lErr != nil {
			if errors.Is(lErr, fs.ErrNotExist) {
				return BaseCapture{}, false, nil
			}
			return BaseCapture{}, false, lErr
		}
		xattrs, _ := provider.ListXattr(qpath.Rel)
		if retryHook != nil {
			retryHook(qpath, attempt)
		}
		c.counters.lowerLookups.Add(1)
		after, aErr := provider.Lstat(qpath.Rel)
		if aErr != nil || !sameLowerIdentity(before, after) || after.LinkTarget != target {
			return BaseCapture{}, false, nil
		}
		return BaseCapture{
			Path:       qpath,
			Layer:      LayerTrunk,
			Kind:       EntrySymlink,
			Mode:       before.Mode,
			ModTime:    before.ModTime,
			LinkTarget: target,
			Xattrs:     xattrs,
			Ino:        before.Ino,
			CapturedAt: time.Now(),
		}, true, nil

	case EntryRegular:
		return tryCaptureRegularOnce(c, qpath, provider, before, attempt, retryHook)

	default:
		return BaseCapture{}, false, ErrUnsupportedOperation
	}
}

func tryCaptureRegularOnce(
	c *Core,
	qpath QualifiedPath,
	provider LowerProvider,
	before LowerEntry,
	attempt int,
	retryHook func(QualifiedPath, int),
) (BaseCapture, bool, error) {
	rc, openedEntry, err := provider.OpenRead(qpath.Rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return BaseCapture{}, false, nil
		}
		return BaseCapture{}, false, err
	}
	if !sameLowerIdentity(before, openedEntry) {
		_ = rc.Close()
		return BaseCapture{}, false, nil
	}

	hookReader := io.Reader(rc)
	if retryHook != nil {
		hookReader = &midReadHookReader{
			r:         rc,
			totalSize: before.Size,
			onMidway: func() {
				retryHook(qpath, attempt)
			},
		}
	}

	baseName, basePath, sha, copiedSize, wErr := writeBaseStream(c, hookReader)
	_ = rc.Close()
	if wErr != nil {
		return BaseCapture{}, false, wErr
	}

	xattrs, _ := provider.ListXattr(qpath.Rel)
	if retryHook != nil && before.Size == 0 {
		retryHook(qpath, attempt)
	}

	c.counters.lowerLookups.Add(1)
	baseDirFD := int(c.backing.baseDirFile.Fd())
	after, aErr := provider.Lstat(qpath.Rel)
	if aErr != nil || !sameLowerIdentity(before, after) || copiedSize != before.Size || copiedSize != after.Size {
		_ = unlinkBackingFile(baseDirFD, baseName)
		return BaseCapture{}, false, nil
	}

	if !verifySourceAndBaseDigest(provider, qpath.Rel, after, baseDirFD, baseName, basePath, copiedSize, sha) {
		_ = unlinkBackingFile(baseDirFD, baseName)
		return BaseCapture{}, false, nil
	}

	return BaseCapture{
		Path:        qpath,
		Layer:       LayerTrunk,
		Kind:        EntryRegular,
		Mode:        before.Mode,
		Size:        copiedSize,
		ModTime:     before.ModTime,
		SHA256:      sha,
		Xattrs:      xattrs,
		Ino:         before.Ino,
		BackingFile: basePath,
		CapturedAt:  time.Now(),
	}, true, nil
}

func verifySourceAndBaseDigest(
	provider LowerProvider,
	rel string,
	after LowerEntry,
	baseDirFD int,
	baseName, basePath string,
	copiedSize int64,
	sha string,
) bool {
	rcVerify, verifyEntry, vErr := provider.OpenRead(rel)
	if vErr != nil || !sameLowerIdentity(after, verifyEntry) {
		if rcVerify != nil {
			_ = rcVerify.Close()
		}
		return false
	}
	verifyHash := sha256.New()
	verifyN, copyErr := io.Copy(verifyHash, rcVerify)
	_ = rcVerify.Close()
	if copyErr != nil || verifyN != copiedSize || hex.EncodeToString(verifyHash.Sum(nil)) != sha {
		return false
	}

	baseCheck, bErr := openBackingFileRO(baseDirFD, baseName, basePath)
	if bErr != nil {
		return false
	}
	baseHash := sha256.New()
	baseN, bCopyErr := io.Copy(baseHash, baseCheck)
	_ = baseCheck.Close()
	return bCopyErr == nil && baseN == copiedSize && hex.EncodeToString(baseHash.Sum(nil)) == sha
}

type midReadHookReader struct {
	r         io.Reader
	totalSize int64
	readBytes int64
	fired     bool
	onMidway  func()
}

func (m *midReadHookReader) Read(p []byte) (int, error) {
	if !m.fired && m.totalSize > 1 && m.readBytes == 0 {
		half := m.totalSize / 2
		if half > 0 && int64(len(p)) > half {
			p = p[:half]
		}
	}
	n, err := m.r.Read(p)
	m.readBytes += int64(n)
	if !m.fired && (m.readBytes >= m.totalSize/2 || err != nil) {
		m.fired = true
		if m.onMidway != nil {
			m.onMidway()
		}
	}
	return n, err
}

func sameLowerIdentity(a, b LowerEntry) bool {
	return a.Kind == b.Kind &&
		a.Dev == b.Dev &&
		a.Ino == b.Ino &&
		a.Size == b.Size &&
		a.Mode == b.Mode &&
		a.ModTime.Equal(b.ModTime) &&
		a.CtimeSec == b.CtimeSec &&
		a.CtimeNsec == b.CtimeNsec
}

func initWorkingFromBase(c *Core, base BaseCapture) (string, string, error) {
	c.mu.Lock()
	if c.state.released {
		c.mu.Unlock()
		return "", "", ErrViewReleased
	}
	workName, workPath := c.state.allocStorageFile(c.backing.workDir, "work")
	workDirFD := int(c.backing.workDirFile.Fd())
	c.mu.Unlock()

	if base.BackingFile != "" {
		cloned, cloneErr := cloneFileIfSupported(base.BackingFile, workPath)
		if cloneErr == nil && cloned {
			return workName, workPath, nil
		}
		baseFile, err := openBackingFileRO(int(c.backing.baseDirFile.Fd()), filepath.Base(base.BackingFile), base.BackingFile)
		if err != nil {
			return "", "", err
		}
		defer func() { _ = baseFile.Close() }()

		wf, err := createExclusiveBackingFile(workDirFD, workName, workPath, 0o600)
		if err != nil {
			return "", "", err
		}
		_, copyErr := io.Copy(wf, baseFile)
		closeErr := wf.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			_ = unlinkBackingFile(workDirFD, workName)
			return "", "", err
		}
		return workName, workPath, nil
	}

	wf, err := createExclusiveBackingFile(workDirFD, workName, workPath, 0o600)
	if err != nil {
		return "", "", err
	}
	if err := wf.Close(); err != nil {
		_ = unlinkBackingFile(workDirFD, workName)
		return "", "", err
	}
	return workName, workPath, nil
}

func cloneXattrs(in map[string][]byte) map[string][]byte {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		out[k] = append([]byte(nil), v...)
	}
	return out
}
