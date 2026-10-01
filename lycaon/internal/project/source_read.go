package project

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/textfile"
)

// SourceReadMaxBytes bounds the encoded file, including its byte-order mark.
const SourceReadMaxBytes = sourceblob.MaxRevisionContentBytes

var (
	// ErrSourcePathInvalid is an empty or malformed path / line query.
	ErrSourcePathInvalid = errors.New("source path invalid")
	// ErrSourcePathDenied is outside the project sandbox jail.
	ErrSourcePathDenied = errors.New("source path denied")
	// ErrSourceNotFound is a missing path or non-regular file under the jail.
	ErrSourceNotFound = errors.New("source not found")
	// ErrSourceBinary rejects binary content where text is required.
	ErrSourceBinary = errors.New("source binary denied")
	// ErrSourceUnsupportedEncoding rejects undecodable text.
	ErrSourceUnsupportedEncoding = errors.New("source unsupported encoding")
	// ErrSourceDecodeAsInvalid rejects an unsupported explicit encoding.
	ErrSourceDecodeAsInvalid = errors.New("source decode-as encoding invalid")
	// ErrSourceNoRoot means the project has no attached folders.
	ErrSourceNoRoot = errors.New("source no project root")
	// ErrSourceRawTooLarge is a raw image over SourceRawMaxBytes (HTTP 413).
	ErrSourceRawTooLarge = errors.New("source raw too large")
	// ErrSourceRawNotImage is a raw request for a non-image payload (HTTP 415).
	ErrSourceRawNotImage = errors.New("source raw not image")
)

// SourceUnsupportedEncodingError names the detected encoding for a 415 refusal.
type SourceUnsupportedEncodingError struct {
	Detected string
}

func (e *SourceUnsupportedEncodingError) Error() string {
	if e == nil || e.Detected == "" {
		return ErrSourceUnsupportedEncoding.Error()
	}
	return ErrSourceUnsupportedEncoding.Error() + ": " + e.Detected
}

func (e *SourceUnsupportedEncodingError) Unwrap() error {
	return ErrSourceUnsupportedEncoding
}

// SourceRevision is exact content from one bounded observation.
type SourceRevision struct {
	Bytes  []byte
	SHA256 string
}

// SourceReadObservation is the host fact behind one source projection.
type SourceReadObservation struct {
	Path      string
	RootID    string
	Revision  SourceRevision
	OverLimit bool
	SizeBytes int64
	MTime     time.Time
	MIME      string
	Writable  bool
	sniff     []byte
	image     bool
	decodeAs  string
}

// SourceReadResult is sandbox-validated file content or metadata for GET …/source.
type SourceReadResult struct {
	Path      string
	Content   string
	FileID    string
	VersionID string
	// OverLimit leaves Content empty rather than truncating it.
	OverLimit bool
	// Binary leaves Content empty; images use the raw endpoint.
	Binary    bool
	SizeBytes int64
	MTime     time.Time
	MIME      string
	// SHA256 is the editable text revision used for write conflicts.
	SHA256   string
	RootID   string
	Encoding string
	Writable bool
}

// SourceReadRequest addresses a file within the supplied project's resolved roots.
type SourceReadRequest struct {
	Path   string
	RootID string
	// DecodeAs is a human-selected BOM-less UTF-16 encoding.
	DecodeAs string
}

type sourceReadTarget struct {
	abs, path, rootID string
	info              os.FileInfo
}

func resolveSourceReadTarget(p *Project, pathQuery, rootID string) (sourceReadTarget, error) {
	bases, err := sourceSearchBases(p, rootID)
	if err != nil {
		return sourceReadTarget{}, err
	}
	normalized := false
	for _, base := range bases {
		abs, path, ok := evidence.ResolveCitationAbs(base.Dir, pathQuery)
		if !ok {
			continue
		}
		normalized = true
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return sourceReadTarget{}, fmt.Errorf("stat source: %w", err)
		}
		if info.Mode().IsRegular() {
			return sourceReadTarget{abs: abs, path: path, rootID: base.RootID, info: info}, nil
		}
	}
	if normalized {
		return sourceReadTarget{}, ErrSourceNotFound
	}
	return sourceReadTarget{}, ErrSourcePathDenied
}

// ObserveProjectSource reads one source fact under the citation jail.
func ObserveProjectSource(p *Project, req SourceReadRequest) (*SourceReadObservation, error) {
	if p == nil {
		return nil, ErrSourceNoRoot
	}
	pathQuery := strings.TrimSpace(req.Path)
	if pathQuery == "" {
		return nil, ErrSourcePathInvalid
	}
	if len(p.Roots) == 0 {
		return nil, ErrSourceNoRoot
	}
	if req.DecodeAs != "" && req.DecodeAs != textfile.UTF16LE && req.DecodeAs != textfile.UTF16BE {
		return nil, ErrSourceDecodeAsInvalid
	}

	target, err := resolveSourceReadTarget(p, pathQuery, req.RootID)
	if err != nil {
		return nil, err
	}

	size := target.info.Size()
	overLimit := size > SourceReadMaxBytes

	f, err := os.Open(target.abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrSourceNotFound
		}
		return nil, fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = f.Close() }()

	sniffLen := int64(sourceSniffPrefixBytes)
	if size < sniffLen {
		sniffLen = size
	}
	sniffBuf := make([]byte, sniffLen)
	n, err := io.ReadFull(f, sniffBuf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("read source: %w", err)
	}
	sniffBuf = sniffBuf[:n]
	mime, image := sniffSourceContent(sniffBuf)
	writable := SourceFileWritable(uint32(target.info.Mode().Perm()))

	observation := &SourceReadObservation{
		Path:      target.path,
		OverLimit: overLimit,
		SizeBytes: size,
		MTime:     target.info.ModTime().UTC(),
		MIME:      mime,
		RootID:    target.rootID,
		Writable:  writable,
		sniff:     sniffBuf,
		image:     image,
		decodeAs:  req.DecodeAs,
	}

	var revision []byte
	if !overLimit {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("seek source revision: %w", err)
		}
		revision = make([]byte, size)
		n, err = io.ReadFull(f, revision)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("read source revision: %w", err)
		}
		revision = revision[:n]
		observation.SizeBytes = int64(len(revision))
		observation.Revision = SourceRevision{Bytes: revision, SHA256: textfile.SHA256(revision)}
	}
	return observation, nil
}

// Project builds the editor-facing result from the observed source fact.
func (o *SourceReadObservation) Project() (*SourceReadResult, error) {
	if o == nil {
		return nil, ErrSourcePathInvalid
	}
	out := &SourceReadResult{
		Path:      o.Path,
		OverLimit: o.OverLimit,
		SizeBytes: o.SizeBytes,
		MTime:     o.MTime,
		MIME:      o.MIME,
		RootID:    o.RootID,
		Writable:  o.Writable,
	}

	// Images use metadata and the raw endpoint.
	if o.image {
		out.Binary = true
		return out, nil
	}

	raw := o.Revision.Bytes
	if o.OverLimit {
		// The bounded sniff may end inside a character; full revisions stay strict.
		raw = textfile.TrimIncompleteTail(o.sniff)
	}
	classify, err := sourceReadClassification(raw, o.decodeAs)
	if err != nil {
		return nil, err
	}
	if classify.Class == textfile.Binary {
		out.Binary = true
		out.MIME = firstNonEmpty(out.MIME, SourceMIMEOctet)
		return out, nil
	}

	// Oversized text retains its detected encoding.
	if o.OverLimit {
		out.Encoding = classify.Encoding
		out.MIME = sourceTextMIME(out.MIME)
		return out, nil
	}

	var doc textfile.UntrustedDocument
	var detection textfile.Detection
	if o.decodeAs != "" {
		doc, detection, err = textfile.OpenAsUTF16WithoutBOM(o.Revision.Bytes, o.decodeAs, textfile.LimitsForRaw(SourceReadMaxBytes))
	} else {
		doc, detection, err = textfile.Open(o.Revision.Bytes, textfile.LimitsForRaw(SourceReadMaxBytes))
	}
	if err != nil {
		if detection.Class == textfile.Unsupported {
			return nil, &SourceUnsupportedEncodingError{Detected: detection.Detected}
		}
		if detection.Class == textfile.Binary {
			out.Binary = true
			out.MIME = firstNonEmpty(out.MIME, SourceMIMEOctet)
			out.Content = ""
			return out, nil
		}
		return nil, &SourceUnsupportedEncodingError{Detected: textfile.Unknown}
	}
	out.Encoding = doc.Encoding()
	out.MIME = sourceTextMIME(out.MIME)
	out.SHA256 = doc.RawSHA256()
	out.Content = doc.Text()
	return out, nil
}

// ReadProjectSource reads and projects one repo-relative file.
func ReadProjectSource(p *Project, req SourceReadRequest) (*SourceReadResult, error) {
	observation, err := ObserveProjectSource(p, req)
	if err != nil {
		return nil, err
	}
	return observation.Project()
}

// sourceReadClassification applies explicit UTF-16 decoding without inference.
func sourceReadClassification(raw []byte, decodeAs string) (textfile.Detection, error) {
	detection := textfile.Classify(raw)
	if decodeAs != "" {
		if detection.Class == textfile.Supported {
			return textfile.Detection{}, ErrSourceDecodeAsInvalid
		}
		return textfile.Detection{Class: textfile.Supported, Encoding: decodeAs}, nil
	}
	if detection.Class == textfile.Unsupported {
		return textfile.Detection{}, &SourceUnsupportedEncodingError{Detected: detection.Detected}
	}
	return detection, nil
}

// SourceRawResult is a jailed image payload for GET …/source/raw.
type SourceRawResult struct {
	Path        string
	RootID      string
	ContentType string
	Bytes       []byte
	SizeBytes   int64
	MTime       time.Time
}

// ReadProjectSourceRaw reads a jailed image payload.
func ReadProjectSourceRaw(p *Project, req SourceReadRequest) (*SourceRawResult, error) {
	if p == nil {
		return nil, ErrSourceNoRoot
	}
	pathQuery := strings.TrimSpace(req.Path)
	if pathQuery == "" {
		return nil, ErrSourcePathInvalid
	}
	if len(p.Roots) == 0 {
		return nil, ErrSourceNoRoot
	}
	target, err := resolveSourceReadTarget(p, pathQuery, req.RootID)
	if err != nil {
		return nil, err
	}
	size := target.info.Size()
	if size > SourceRawMaxBytes {
		return nil, ErrSourceRawTooLarge
	}

	f, err := os.Open(target.abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrSourceNotFound
		}
		return nil, fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = f.Close() }()

	sniffLen := int64(sourceSniffPrefixBytes)
	if size < sniffLen {
		sniffLen = size
	}
	sniffBuf := make([]byte, sniffLen)
	n, err := io.ReadFull(f, sniffBuf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("read source: %w", err)
	}
	sniffBuf = sniffBuf[:n]
	mime, image := sniffSourceContent(sniffBuf)
	if !image || !isSourceImageMIME(mime) {
		return nil, ErrSourceRawNotImage
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek source: %w", err)
	}
	buf := make([]byte, size)
	n, err = io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("read source: %w", err)
	}
	buf = buf[:n]
	// Revalidate the complete payload before serving image bytes.
	fullMIME, fullImage := sniffSourceContent(buf)
	if !fullImage || !isSourceImageMIME(fullMIME) {
		return nil, ErrSourceRawNotImage
	}
	return &SourceRawResult{
		Path:        target.path,
		RootID:      target.rootID,
		ContentType: fullMIME,
		Bytes:       buf,
		SizeBytes:   size,
		MTime:       target.info.ModTime().UTC(),
	}, nil
}

func sourceSearchBases(p *Project, rootID string) ([]sourceBase, error) {
	roots, err := scopedRootsForSource(p, rootID)
	if err != nil {
		return nil, err
	}
	bases := make([]sourceBase, 0, len(roots))
	for _, root := range roots {
		bases = append(bases, sourceBase{Dir: root.Path, RootID: root.ID})
	}
	return bases, nil
}

type sourceBase struct {
	Dir    string
	RootID string
}

func scopedRootsForSource(p *Project, rootID string) ([]Root, error) {
	ordered := orderedRootsForSource(p)
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return ordered, nil
	}
	for _, r := range ordered {
		if r.ID == rootID {
			return []Root{r}, nil
		}
	}
	return nil, ErrSourceNotFound
}

func orderedRootsForSource(p *Project) []Root {
	if p == nil || len(p.Roots) == 0 {
		return nil
	}
	out := make([]Root, 0, len(p.Roots))
	var primary *Root
	for i := range p.Roots {
		r := p.Roots[i]
		if r.IsPrimary {
			cp := r
			primary = &cp
			continue
		}
		out = append(out, r)
	}
	if primary != nil {
		return append([]Root{*primary}, out...)
	}
	return append([]Root{}, p.Roots...)
}
