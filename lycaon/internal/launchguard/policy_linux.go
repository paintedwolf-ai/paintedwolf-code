//go:build linux

package launchguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// WritePolicy atomically selects a verified generation. The installer calls it
// while retaining its full-payload verification and shared generation lock.
func WritePolicy(policy GenerationPolicy) error {
	if os.Geteuid() != 0 {
		return errors.New("launch policy selection requires administrator authority")
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	if _, err := decodePolicy(raw); err != nil {
		return err
	}
	parent, err := openRootControlled(filepath.Dir(PolicyPath), true)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	lifetime, err := retainGeneration(policy.ManifestSHA256)
	if err != nil {
		return err
	}
	defer func() { _ = lifetime.Close() }()
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: filepath.Dir(PolicyPath), Rel: filepath.Base(PolicyPath)},
		Source:   bytes.NewReader(append(raw, '\n')), Mode: 0644,
		BeforeCommit: func(target fseffect.Target, _ fseffect.Result) error {
			info, err := target.Lstat()
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("existing launch policy is not a regular file")
			}
			return trustedObject(info)
		},
	})
	return err
}
