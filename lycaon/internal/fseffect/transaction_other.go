//go:build !darwin && !linux && !windows

package fseffect

import (
	"os"
	"time"
)

func OpenRead(Location) (*os.File, error)                     { return nil, ErrUnsupported }
func OpenWrite(Location, bool, os.FileMode) (*os.File, error) { return nil, ErrUnsupported }

func stageReplace(ReplaceRequest, func(stage) error) (*Replacement, Result, error) {
	return nil, Result{}, ErrUnsupported
}
func Remove(RemoveRequest) error                  { return ErrUnsupported }
func Rename(string, string, string) error         { return ErrUnsupported }
func MkdirAll(Location, os.FileMode) (int, error) { return 0, ErrUnsupported }
func UpdateMode(ModeUpdateRequest) (ModeUpdateResult, error) {
	return ModeUpdateResult{}, ErrUnsupported
}
func platformChown(Location, int, int) error { return ErrUnsupported }

func platformTimes(Location, *time.Time, *time.Time) error { return ErrUnsupported }

func RenameNoReplace(string, string, string) error { return ErrUnsupported }

func RenameGuarded(string, string, string, string) error { return ErrUnsupported }

func RemoveTreeGuarded(Location, string) error { return ErrUnsupported }

func SyncDirectory(*os.Root, string) error { return ErrUnsupported }

func openGuardedDirectory(Location, string) (*os.File, error) { return nil, ErrUnsupported }

// ReadRoot is unsupported on this platform.
type ReadRoot struct{}

func OpenReadRoot(string) (*ReadRoot, error)    { return nil, ErrUnsupported }
func (*ReadRoot) Path() string                  { return "" }
func (*ReadRoot) Open(string) (*os.File, error) { return nil, ErrUnsupported }
func (*ReadRoot) Close() error                  { return nil }

func RelocateGuarded(Location, Location, string) error { return ErrUnsupported }

func platformAccessTime(os.FileInfo) (time.Time, bool) { return time.Time{}, false }
