//go:build linux

package packagegeneration

import (
	"encoding/hex"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

func openLifetime(directory, identity string, owner uint32) (*os.File, error) {
	decoded, err := hex.DecodeString(identity)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != identity {
		return nil, errors.New("native generation: invalid identity")
	}
	if err := controlledParents(directory, owner); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(directory, identity+".lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil {
		err = controlledObject(info, owner)
	}
	if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600) {
		err = errors.New("native generation: unsafe lifetime lock")
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
