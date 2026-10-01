package bundled

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/fseffect"
)

const maxReleaseExpandedBytes int64 = 2 << 30

type releaseArchive struct {
	file *os.File
	pin  *ReleaseArtifact
}

func openPinnedArchive(ctx context.Context, path string, pin *ReleaseArtifact) (*releaseArchive, error) {
	file, err := openReleaseFile(path)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(&releaseContextReader{ctx: ctx, reader: file}, pin.Bytes+1))
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if size != pin.Bytes || hex.EncodeToString(hash.Sum(nil)) != pin.SHA256 {
		_ = file.Close()
		return nil, fmt.Errorf("cached release archive does not match its pin")
	}
	return &releaseArchive{file: file, pin: pin}, nil
}

func releaseMemberNames() map[string]bool {
	names := map[string]bool{"opengrep": true}
	for _, name := range artifactPayloadNames {
		names[name] = true
	}
	return names
}

type releaseArchiveReader struct {
	compressed *bufio.Reader
	gzip       *gzip.Reader
	expanded   *releaseCountingReader
	tar        *tar.Reader
}

func newReleaseArchiveReader(reader io.Reader) (*releaseArchiveReader, error) {
	compressed := bufio.NewReader(reader)
	unzipped, err := gzip.NewReader(compressed)
	if err != nil {
		return nil, err
	}
	unzipped.Multistream(false)
	expanded := &releaseCountingReader{reader: io.LimitReader(unzipped, maxReleaseExpandedBytes+1)}
	return &releaseArchiveReader{compressed: compressed, gzip: unzipped, expanded: expanded, tar: tar.NewReader(expanded)}, nil
}

type releaseCountingReader struct {
	reader io.Reader
	bytes  int64
}

func (r *releaseCountingReader) Read(data []byte) (int, error) {
	n, err := r.reader.Read(data)
	r.bytes += int64(n)
	return n, err
}

func (archive *releaseArchive) scan(ctx context.Context, directory string) (map[string]PayloadIdentity, error) {
	if _, err := archive.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	hash := sha256.New()
	stream, err := newReleaseArchiveReader(io.TeeReader(&releaseContextReader{ctx: ctx, reader: archive.file}, hash))
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.gzip.Close() }()
	members, minimum, err := stream.readMembers(directory)
	if err != nil {
		return nil, err
	}
	if err := stream.finish(minimum); err != nil {
		return nil, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != archive.pin.SHA256 {
		return nil, fmt.Errorf("release archive changed while reading")
	}
	return members, nil
}

func (stream *releaseArchiveReader) readMembers(directory string) (map[string]PayloadIdentity, int64, error) {
	expected := releaseMemberNames()
	members := make(map[string]PayloadIdentity)
	minimum := int64(1024)
	for {
		header, err := stream.tar.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, err
		}
		if err := validateReleaseHeader(header, expected, members); err != nil {
			return nil, 0, err
		}
		minimum += 512 + ((header.Size+511)/512)*512
		if minimum > maxReleaseExpandedBytes {
			return nil, 0, fmt.Errorf("release archive exceeds expanded size limit")
		}
		member, err := readReleaseMember(stream.tar, header, directory)
		if err != nil {
			return nil, 0, err
		}
		members[header.Name] = member
	}
	if len(members) != len(expected) {
		return nil, 0, fmt.Errorf("release archive requires every executable, source and qualification payload")
	}
	return members, minimum, nil
}

func validateReleaseHeader(header *tar.Header, expected map[string]bool, members map[string]PayloadIdentity) error {
	_, duplicate := members[header.Name]
	if !expected[header.Name] || duplicate || header.Typeflag != tar.TypeReg || header.Linkname != "" || len(header.PAXRecords) != 0 || header.Format != tar.FormatUSTAR {
		return fmt.Errorf("release archive contains an unexpected, duplicate or nonregular member")
	}
	if header.Size <= 0 || header.Size > maxArtifactBytes || header.Mode&0o7000 != 0 {
		return fmt.Errorf("invalid release member size or mode")
	}
	return nil
}

func readReleaseMember(reader io.Reader, header *tar.Header, directory string) (PayloadIdentity, error) {
	var result fseffect.Result
	var err error
	if directory != "" {
		mode := os.FileMode(0o644)
		if header.Name == "opengrep" {
			mode = 0o755
		}
		result, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: directory, Rel: header.Name}, Source: reader, Mode: mode})
	} else {
		hash := sha256.New()
		result.Bytes, err = io.Copy(hash, reader)
		result.SHA256 = hex.EncodeToString(hash.Sum(nil))
	}
	if err != nil {
		return PayloadIdentity{}, err
	}
	if result.Bytes != header.Size {
		return PayloadIdentity{}, fmt.Errorf("truncated release member")
	}
	return PayloadIdentity{Name: header.Name, SHA256: result.SHA256, Bytes: result.Bytes}, nil
}

func (stream *releaseArchiveReader) finish(minimum int64) error {
	buffer := make([]byte, 32<<10)
	for {
		count, err := stream.expanded.Read(buffer)
		for _, value := range buffer[:count] {
			if value != 0 {
				return fmt.Errorf("release archive has nonzero trailing content")
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	if stream.expanded.bytes < minimum || stream.expanded.bytes > maxReleaseExpandedBytes || stream.expanded.bytes%512 != 0 {
		return fmt.Errorf("release archive is truncated or oversized")
	}
	if _, err := stream.compressed.ReadByte(); err != io.EOF {
		return fmt.Errorf("release archive has trailing compressed content")
	}
	return nil
}

func verifyReleaseMembers(m *Manifest, members map[string]PayloadIdentity) error {
	if m.identity == nil {
		return fmt.Errorf("release artifact has no admitted identity")
	}
	binary := members["opengrep"]
	if binary.SHA256 != m.identity.BinarySHA256 || binary.Bytes != m.identity.BinaryBytes {
		return fmt.Errorf("release executable differs from pinned archive")
	}
	for _, part := range m.identity.Payload {
		if members[part.Name] != part {
			return fmt.Errorf("release payload %s differs from pinned archive", part.Name)
		}
	}
	return nil
}
