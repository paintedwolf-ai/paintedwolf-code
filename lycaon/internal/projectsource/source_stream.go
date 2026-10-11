package projectsource

import (
	"errors"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/textfile"
)

// SourceStream pins a jailed file descriptor while a reader captures its revision.
type SourceStream struct {
	File                   *os.File
	Path, RootID, Encoding string
	SizeBytes              int64
	root                   *os.Root
	info                   os.FileInfo
	changeTime             [2]int64
}

func OpenProjectSourceStream(p ProjectSource, req SourceReadRequest) (*SourceStream, error) {
	if req.DecodeAs != "" && req.DecodeAs != textfile.UTF16LE && req.DecodeAs != textfile.UTF16BE {
		return nil, ErrSourceDecodeAsInvalid
	}
	target, err := resolveSourceReadTarget(p, req.Path, req.RootID)
	if err != nil {
		return nil, err
	}
	var rootPath string
	for _, root := range p.SourceRoots() {
		if root.ID == target.rootID {
			rootPath = root.Path
			break
		}
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	file, err := root.Open(target.path)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	stream := &SourceStream{File: file, root: root, Path: target.path, RootID: target.rootID}
	ok := false
	defer func() {
		if !ok {
			stream.Close()
		}
	}()
	stream.info, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if !stream.info.Mode().IsRegular() {
		return nil, ErrSourceNotFound
	}
	stream.changeTime, err = sourceChangeTime(file, stream.info)
	if err != nil {
		return nil, err
	}
	stream.SizeBytes = stream.info.Size()
	prefix := make([]byte, sourceSniffPrefixBytes)
	n, err := io.ReadFull(file, prefix)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	detection, err := sourceReadClassification(textfile.TrimIncompleteTail(prefix[:n]), req.DecodeAs)
	if err != nil {
		return nil, err
	}
	if detection.Class != textfile.Supported {
		return nil, ErrSourceBinary
	}
	stream.Encoding = detection.Encoding
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	ok = true
	return stream, nil
}

// Current rejects replacement, in-place writes, truncation, and missing paths.
func (s *SourceStream) Current() bool {
	current, err := s.root.Stat(s.Path)
	if err != nil || !os.SameFile(s.info, current) || s.info.Size() != current.Size() || !s.info.ModTime().Equal(current.ModTime()) {
		return false
	}
	stamp, err := sourceChangeTime(s.File, current)
	return err == nil && stamp == s.changeTime
}

func (s *SourceStream) Close() {
	_ = s.File.Close()
	_ = s.root.Close()
}
