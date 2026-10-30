//go:build !darwin && !linux && !windows

package fseffect

import "os"

func (*ReadRoot) Canonical(string) (string, error)                    { return "", ErrUnsupported }
func (*ReadRoot) Readlink(string, func(string) error) (string, error) { return "", ErrUnsupported }

func (*ReadRoot) OpenExact(string) (*os.File, error) { return nil, ErrUnsupported }

// Native metadata-only execution handles need the corresponding platform adapter.
type MetadataRoot struct{}

func OpenMetadataRoot(string) (*MetadataRoot, error)           { return nil, ErrUnsupported }
func (*MetadataRoot) Path() string                             { return "" }
func (*MetadataRoot) OpenExact(string, bool) (*os.File, error) { return nil, ErrUnsupported }
func (*MetadataRoot) Close() error                             { return ErrUnsupported }

func (*ReadRoot) Stat(string, bool, func(string) error) (EntryStat, error) {
	return EntryStat{}, ErrUnsupported
}

func (*ReadRoot) DirectoryPath(*os.File) (string, error)                { return "", ErrUnsupported }
func (*ReadRoot) OpenChildDirectory(*os.File, string) (*os.File, error) { return nil, ErrUnsupported }

func (*ReadRoot) DescriptorOpens() int64 { return 0 }

func (*ReadRoot) DirectoryIdentityDescriptors() (int64, int64) { return 0, 0 }
