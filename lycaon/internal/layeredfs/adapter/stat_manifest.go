package adapter

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

// StatManifestFormat is the stat manifest's durable format. A manifest of any other format is
// refused, and its branch is left as it is.
const StatManifestFormat = 1

// racyWindow is how close a file's mtime may come to the moment its stat was recorded before the
// stat stops proving anything: an edit inside the timestamp granularity leaves the same mtime.
const racyWindow = 2 * time.Second

// ErrUnsupportedStatManifest refuses a manifest this engine cannot read.
var ErrUnsupportedStatManifest = errors.New("unsupported stat manifest format")

// statRecord is what a stat said about one entry. It names candidates for capture and never
// establishes a base: bases are read from the pristine tree.
type statRecord struct {
	Root       string              `json:"r"`
	Rel        string              `json:"p"`
	Kind       layeredfs.EntryKind `json:"k"`
	Mode       uint32              `json:"m"`
	Size       int64               `json:"s,omitempty"`
	MtimeNS    int64               `json:"t,omitempty"`
	CtimeNS    int64               `json:"c,omitempty"`
	Ino        uint64              `json:"i,omitempty"`
	LinkTarget string              `json:"l,omitempty"`
	// recordedNS is when a capture refreshed this record. Zero means the manifest's own time.
	recordedNS int64
}

func (r statRecord) qualified() layeredfs.QualifiedPath {
	return layeredfs.QualifiedPath{RootID: r.Root, Rel: r.Rel}
}

func (r statRecord) fileMode() fs.FileMode { return fs.FileMode(r.Mode) }

func (r statRecord) modTime() time.Time { return time.Unix(0, r.MtimeNS) }

func recordStat(rootID, rel string, kind layeredfs.EntryKind, info fs.FileInfo) statRecord {
	id := statIdentityOf(info)
	return statRecord{
		Root: rootID, Rel: rel, Kind: kind, Mode: uint32(info.Mode()), Size: info.Size(),
		MtimeNS: info.ModTime().UnixNano(), CtimeNS: id.ctimeNS, Ino: id.inode,
	}
}

// unchangedBy reports that info is the stat this record holds, and that the record's own time is far
// enough from its mtime for that to mean the content is the same.
func (r statRecord) unchangedBy(info fs.FileInfo, manifestNS int64) bool {
	id := statIdentityOf(info)
	if info.Size() != r.Size || info.ModTime().UnixNano() != r.MtimeNS || id.ctimeNS != r.CtimeNS ||
		id.inode != r.Ino || uint32(info.Mode()) != r.Mode {
		return false
	}
	recorded := r.recordedNS
	if recorded == 0 {
		recorded = manifestNS
	}
	return r.MtimeNS < recorded-racyWindow.Nanoseconds()
}

// manifestRoot maps one root of a view to its two trees.
type manifestRoot struct {
	RootID        string `json:"root_id"`
	Label         string `json:"label,omitempty"`
	SourcePath    string `json:"source_path"`
	Subroot       string `json:"subroot,omitempty"`
	WorkspacePath string `json:"workspace_path"`
	PristinePath  string `json:"pristine_path"`
	IsPrimary     bool   `json:"is_primary,omitempty"`
}

// statManifestHeader is the first line of a manifest file; one statRecord follows per line.
type statManifestHeader struct {
	Format  int            `json:"format"`
	ViewID  string         `json:"view_id"`
	TakenNS int64          `json:"taken_ns"`
	Roots   []manifestRoot `json:"roots"`
}

func writeStatManifest(path string, header statManifestHeader, records []statRecord) error {
	pipeRead, pipeWrite := io.Pipe()
	go func() {
		buffered := bufio.NewWriter(pipeWrite)
		enc := json.NewEncoder(buffered)
		err := enc.Encode(header)
		for i := 0; err == nil && i < len(records); i++ {
			err = enc.Encode(records[i])
		}
		if err == nil {
			err = buffered.Flush()
		}
		_ = pipeWrite.CloseWithError(err)
	}()
	defer func() { _ = pipeRead.Close() }()
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path), Source: pipeRead, Mode: 0o600, DirMode: 0o700,
	})
	return err
}

func loadStatManifest(path string) (statManifestHeader, []statRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return statManifestHeader{}, nil, err
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(bufio.NewReader(f))
	var header statManifestHeader
	if err := dec.Decode(&header); err != nil {
		return statManifestHeader{}, nil, fmt.Errorf("read stat manifest header: %w", err)
	}
	if header.Format != StatManifestFormat {
		return statManifestHeader{}, nil, fmt.Errorf("%w: %d", ErrUnsupportedStatManifest, header.Format)
	}
	var records []statRecord
	for {
		var rec statRecord
		err := dec.Decode(&rec)
		if errors.Is(err, io.EOF) {
			return header, records, nil
		}
		if err != nil {
			return statManifestHeader{}, nil, fmt.Errorf("read stat manifest: %w", err)
		}
		records = append(records, rec)
	}
}

// HasStatManifest reports whether path holds a manifest, so a caller can tell a materialized branch
// this engine prepared from one an earlier format left, which keeps its baseline.
func HasStatManifest(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// statIdentity is the part of a stat the portable interface does not carry.
type statIdentity struct {
	ctimeNS int64
	inode   uint64
}
