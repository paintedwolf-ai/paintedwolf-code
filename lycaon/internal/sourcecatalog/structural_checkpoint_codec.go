package sourcecatalog

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/repochange"
)

const structuralCheckpointMetadataLimit = 1 << 20

// A checkpoint belongs to a filesystem root, not to the project that attached
// it, so every project on the same path restores from the same file.
type structuralCheckpointHeader struct {
	Format          string
	RootPath        []byte
	Generation      int64
	Directories     int64
	RootObservation DirectoryObservation
}

type structuralCheckpointDirectory struct {
	Page        uint64
	Path        []byte
	Observation DirectoryObservation
}

type structuralCheckpointEncoder struct {
	ctx        context.Context
	writer     io.Writer
	generation *structuralGeneration
	workspace  *structuralCheckpointWorkspace
}

func writeStructuralCheckpoint(ctx context.Context, writer io.Writer, root Root, generation *structuralGeneration) error {
	buffered := bufio.NewWriterSize(writer, 128<<10)
	digest := sha256.New()
	output := io.MultiWriter(buffered, digest)
	workspace, err := newStructuralCheckpointWorkspace(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = workspace.close() }()
	directory, _, err := generation.directories.Get(ctx, ".")
	if err != nil {
		return err
	}
	header := structuralCheckpointHeader{Format: structuralFormat, RootPath: []byte(root.Path), Generation: generation.id, Directories: int64(generation.directories.Len()), RootObservation: directory.observation}
	if err := writeStructuralJSON(output, 'H', header); err != nil {
		return err
	}
	encoder := structuralCheckpointEncoder{ctx: ctx, writer: output, generation: generation, workspace: workspace}
	if err := generation.directories.Visit(ctx, encoder.directory); err != nil {
		return err
	}
	if err := writeStructuralFrame(output, 0, nil); err != nil {
		return err
	}
	if err := writeAll(buffered, digest.Sum(nil)); err != nil {
		return err
	}
	return buffered.Flush()
}

func (encoder *structuralCheckpointEncoder) directory(directory structuralDirectory) error {
	page, err := encoder.page(directory.page, 0)
	if err != nil {
		return err
	}
	record := structuralCheckpointDirectory{Page: page, Path: []byte(directory.observation.Path), Observation: directory.observation}
	record.Observation.Path = ""
	return writeStructuralJSON(encoder.writer, 'D', record)
}

func (encoder *structuralCheckpointEncoder) page(id uint64, depth int) (uint64, error) {
	if id == 0 {
		return 0, nil
	}
	if copied, ok, err := encoder.workspace.beginEncode(encoder.ctx, id); err != nil || ok {
		return copied, err
	}
	if depth > 64 {
		return 0, pagedview.ErrRange
	}
	page, err := encoder.generation.Read(encoder.ctx, id)
	if err != nil {
		return 0, err
	}
	for i := range page.Children {
		page.Children[i].Page, err = encoder.page(page.Children[i].Page, depth+1)
		if err != nil {
			return 0, err
		}
	}
	body, err := encodeRangePage(page)
	if err != nil {
		return 0, err
	}
	if err := writeStructuralFrame(encoder.writer, 'P', body); err != nil {
		return 0, err
	}
	return encoder.workspace.finishEncode(encoder.ctx, id)
}

func writeStructuralJSON(writer io.Writer, kind byte, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(body) > structuralCheckpointMetadataLimit {
		return pagedview.ErrBudget
	}
	return writeStructuralFrame(writer, kind, body)
}

func writeStructuralFrame(writer io.Writer, kind byte, body []byte) error {
	length := uint64(len(body))
	if length > maxStructuralSegmentEntry {
		return pagedview.ErrBudget
	}
	var header [5]byte
	header[0] = kind
	binary.LittleEndian.PutUint32(header[1:], uint32(length))
	if err := writeAll(writer, header[:]); err != nil {
		return err
	}
	return writeAll(writer, body)
}

func readStructuralFrame(ctx context.Context, reader io.Reader) (byte, []byte, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	var header [5]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, nil, err
	}
	length := binary.LittleEndian.Uint32(header[1:])
	limit := uint32(structuralCheckpointMetadataLimit)
	if header[0] == 'P' {
		limit = maxStructuralSegmentEntry
	}
	if length > limit {
		return 0, nil, pagedview.ErrBudget
	}
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	body := make([]byte, length)
	_, err := io.ReadFull(reader, body)
	return header[0], body, err
}

func decodeStructuralJSON(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return pagedview.ErrRange
	}
	return nil
}

// loadStructuralCheckpoint reads the store's checkpoint. With completeOnly, the
// header alone rejects a checkpoint whose root listing never completed before
// any page is read.
func loadStructuralCheckpoint(ctx context.Context, store *indexStore, completeOnly bool) (*structuralGeneration, error) {
	file, err := os.Open(store.structureFile)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, pagedview.ErrBudget
	}
	if completeOnly {
		kind, body, err := readStructuralFrame(ctx, file)
		if err != nil {
			return nil, err
		}
		var header structuralCheckpointHeader
		if kind != 'H' {
			return nil, pagedview.ErrRange
		}
		if err := decodeStructuralJSON(body, &header); err != nil {
			return nil, err
		}
		observed := header.RootObservation
		if !observed.Complete || observed.Failure != "" || !observed.Stamp.known() {
			return nil, errObservationChanged
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
	}
	return readStructuralCheckpoint(ctx, bufio.NewReader(file), store)
}

func readStructuralCheckpoint(ctx context.Context, reader io.Reader, store *indexStore) (*structuralGeneration, error) {
	digest := sha256.New()
	input := io.TeeReader(reader, digest)
	kind, body, err := readStructuralFrame(ctx, input)
	if err != nil {
		return nil, err
	}
	var header structuralCheckpointHeader
	if kind != 'H' {
		return nil, pagedview.ErrRange
	}
	if err := decodeStructuralJSON(body, &header); err != nil {
		return nil, err
	}
	if header.Format != structuralFormat || string(header.RootPath) != store.root.Path || header.Generation < 0 || header.Generation == math.MaxInt64 || header.Directories < 0 {
		return nil, pagedview.ErrRange
	}
	builder, err := newStructuralBuilder(store, nil)
	if err != nil {
		return nil, err
	}
	defer builder.close()
	workspace, err := newStructuralCheckpointWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = workspace.close() }()
	// Restored listings enter the current epoch and invalidation marks; whether
	// they still hold is decided against the directories themselves.
	decoder := structuralCheckpointDecoder{builder: builder, workspace: workspace, mark: store.observationMark, epoch: repochange.CurrentEpoch(store.root.Path)}
	if err := decoder.records(ctx, input, header.Directories); err != nil {
		return nil, err
	}
	if err := verifyStructuralTrailer(reader, digest.Sum(nil)); err != nil {
		return nil, err
	}
	if err := validateStructuralDirectoryItems(ctx, builder); err != nil {
		return nil, err
	}
	var maximum int64
	if err := builder.directories.Visit(ctx, func(directory structuralDirectory) error {
		maximum = max(maximum, directory.observation.Sequence, directory.observation.FirstListed)
		return nil
	}); err != nil {
		return nil, err
	}
	if maximum == math.MaxInt64 {
		return nil, pagedview.ErrBudget
	}
	for previous := structuralObservationSerial.Load(); previous < maximum; previous = structuralObservationSerial.Load() {
		if structuralObservationSerial.CompareAndSwap(previous, maximum) {
			break
		}
	}
	return builder.seal(ctx, header.Generation)
}

func verifyStructuralTrailer(reader io.Reader, want []byte) error {
	var got [sha256.Size]byte
	if _, err := io.ReadFull(reader, got[:]); err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(got[:], want) != 1 {
		return errors.New("structural checkpoint checksum mismatch")
	}
	var trailing [1]byte
	n, err := reader.Read(trailing[:])
	if n != 0 || !errors.Is(err, io.EOF) {
		return errors.New("structural checkpoint has trailing data")
	}
	return nil
}
