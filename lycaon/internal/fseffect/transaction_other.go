//go:build !darwin && !linux && !windows

package fseffect

import (
	"os"
)

func OpenRead(Location) (*os.File, error)                     { return nil, ErrUnsupported }
func OpenWrite(Location, bool, os.FileMode) (*os.File, error) { return nil, ErrUnsupported }
func platformReplace(req ReplaceRequest) (Result, error)      { return replace(req, nil) }

func replace(ReplaceRequest, func(stage) error) (Result, error) {
	return Result{}, ErrUnsupported
}
func Remove(RemoveRequest) error           { return ErrUnsupported }
func Rename(string, string, string) error  { return ErrUnsupported }
func MkdirAll(Location, os.FileMode) error { return ErrUnsupported }
func UpdateMode(ModeUpdateRequest) (ModeUpdateResult, error) {
	return ModeUpdateResult{}, ErrUnsupported
}
func Chown(Location, int, int) error { return ErrUnsupported }

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
