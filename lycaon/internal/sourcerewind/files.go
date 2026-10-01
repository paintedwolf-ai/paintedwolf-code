package sourcerewind

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/textfile"
)

func location(root, path string) (fseffect.Location, error) {
	rel := filepath.FromSlash(path)
	if root == "" || !filepath.IsAbs(root) || !filepath.IsLocal(rel) || rel == "." || strings.Contains(path, "\\") {
		return fseffect.Location{}, fmt.Errorf("invalid rewind location")
	}
	return fseffect.Location{Root: root, Rel: rel}, nil
}

func matchState(root string, v sourceledger.RestorableVersion) error {
	loc, err := location(root, v.Path)
	if err != nil {
		return err
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = rootHandle.Close() }()
	info, statErr := rootHandle.Lstat(loc.Rel)
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("rewind refuses a replaced symlink: %s", v.Path)
	}
	if v.State == "absent" && errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	if statErr != nil {
		return statErr
	}
	f, err := fseffect.OpenRead(loc)
	if v.State == "absent" {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if f != nil {
			_ = f.Close()
		}
		return fmt.Errorf("rewind expected absent path %s", v.Path)
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || v.State != "content" {
		return fmt.Errorf("rewind requires a retained regular file: %s", v.Path)
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(len(v.Content))+1))
	if err != nil {
		return err
	}
	if textfile.SHA256(raw) != v.SHA256 {
		return fmt.Errorf("rewind content changed: %s", v.Path)
	}
	return nil
}

func removeState(root string, v sourceledger.RestorableVersion) error {
	loc, err := location(root, v.Path)
	if err != nil {
		return err
	}
	return fseffect.Remove(fseffect.RemoveRequest{Location: loc, BeforeCommit: func(target fseffect.Target) error {
		// The descriptor held by fseffect confines the last check and unlink.
		file, err := target.Open()
		if err != nil {
			return err
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, int64(len(v.Content))+1))
		_ = file.Close()
		if readErr != nil {
			return readErr
		}
		if textfile.SHA256(raw) != v.SHA256 {
			return fmt.Errorf("rewind file changed: %s", v.Path)
		}
		return nil
	}})
}

func replaceState(root string, before, after sourceledger.RestorableVersion, mode uint32) error {
	loc, err := location(root, after.Path)
	if err != nil {
		return err
	}
	if textfile.SHA256(after.Content) != after.SHA256 {
		return fmt.Errorf("rewind retained content failed integrity verification")
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: loc, Source: bytes.NewReader(after.Content), Mode: os.FileMode(mode), DirMode: 0o755,
		ObserveStagingPath: sourcefeed.NoteHostTemporaryPath,
		BeforeCommit:       func(_ fseffect.Target, _ fseffect.Result) error { return matchState(root, before) },
	})
	return err
}

// A rename can be interrupted after publishing the retained target but before
// removing the source path. Each retry checks both endpoints.
func transition(f File, before, after sourceledger.RestorableVersion) error {
	if before.Path == after.Path {
		if err := matchState(f.RootPath, after); err == nil {
			return nil
		}
		if err := matchState(f.RootPath, before); err != nil {
			return err
		}
		if after.State == "absent" {
			return removeState(f.RootPath, before)
		}
		return replaceState(f.RootPath, before, after, f.Mode)
	}
	if before.State != "content" || after.State != "content" {
		return fmt.Errorf("rewind location transition requires content at both endpoints")
	}
	absent := after
	absent.State = "absent"
	absent.SHA256 = ""
	absent.Content = nil
	if err := matchState(f.RootPath, after); err != nil {
		if err := matchState(f.RootPath, before); err != nil {
			return err
		}
		if err := replaceState(f.RootPath, absent, after, f.Mode); err != nil {
			return err
		}
	}
	absentBefore := before
	absentBefore.State = "absent"
	absentBefore.SHA256 = ""
	absentBefore.Content = nil
	if err := matchState(f.RootPath, absentBefore); err == nil {
		return nil
	}
	return removeState(f.RootPath, before)
}
