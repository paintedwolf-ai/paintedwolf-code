//go:build !darwin && !linux && !windows

package fseffect

import "os"

func (*ReadRoot) Canonical(string) (string, error)                    { return "", ErrUnsupported }
func (*ReadRoot) Readlink(string, func(string) error) (string, error) { return "", ErrUnsupported }

func (*ReadRoot) OpenExact(string) (*os.File, error) { return nil, ErrUnsupported }

// Native metadata-only execution handles need the corresponding platform adapter.
type MetadataRoot struct{}

func OpenMetadataRoot(string) (*MetadataRoot, error)     { return nil, ErrUnsupported }
func (*MetadataRoot) Path() string                       { return "" }
func (*MetadataRoot) OpenExact(string) (*os.File, error) { return nil, ErrUnsupported }
func (*MetadataRoot) Close() error                       { return ErrUnsupported }
