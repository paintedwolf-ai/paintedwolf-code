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
	"path"
	"strings"

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

type structuralCheckpointPage struct {
	branch     pagedview.Branch
	last       string
	parent     string
	references uint8
	height     int
}

type structuralCheckpointDecoder struct {
	builder       *structuralBuilder
	workspace     *structuralCheckpointWorkspace
	directories   int64
	lastDirectory string
	mark          func(string) uint64
	epoch         repochange.Epoch
}

func (decoder *structuralCheckpointDecoder) records(ctx context.Context, reader io.Reader, directoryCount int64) error {
	for {
		kind, body, err := readStructuralFrame(ctx, reader)
		if err != nil {
			return err
		}
		switch kind {
		case 'P':
			if err := decoder.page(ctx, body); err != nil {
				return err
			}
		case 'D':
			if decoder.directories >= directoryCount {
				return pagedview.ErrRange
			}
			if err := decoder.directory(ctx, body); err != nil {
				return err
			}
		case 0:
			if len(body) != 0 || decoder.directories != directoryCount {
				return pagedview.ErrRange
			}
			return decoder.workspace.allReferenced(ctx)
		default:
			return pagedview.ErrRange
		}
	}
}

func (decoder *structuralCheckpointDecoder) page(ctx context.Context, body []byte) error {
	page, err := decodeRangePage(body)
	if err != nil {
		return err
	}
	facts, err := decoder.pageFacts(ctx, &page)
	if err != nil {
		return err
	}
	facts.branch.Page, err = decoder.builder.Write(ctx, 0, page)
	if err != nil {
		return err
	}
	_, err = decoder.workspace.addFact(ctx, facts)
	return err
}

func (decoder *structuralCheckpointDecoder) pageFacts(ctx context.Context, page *pagedview.RangePage[TreeItem]) (structuralCheckpointPage, error) {
	facts := structuralCheckpointPage{height: 1}
	for _, item := range page.Items {
		name := path.Base(item.Value.Path)
		if !validStructuralPath(item.Value.Path) || item.Value.Path == "." || item.Key != DirectoryOrder(name, directoryOrderKind(item.Key)) {
			return facts, pagedview.ErrRange
		}
		parent := path.Dir(item.Value.Path)
		if err := facts.add(pagedview.Branch{Key: item.Key, Weight: item.Weight, Count: 1, Unresolved: item.Unresolved, Fingerprint: item.Fingerprint, BaselineFingerprint: item.BaselineFingerprint}, item.Key, parent); err != nil {
			return facts, err
		}
	}
	for i := range page.Children {
		child := &page.Children[i]
		local := child.Page
		known, err := decoder.workspace.fact(ctx, local)
		if err != nil {
			return facts, err
		}
		expected := known.branch
		expected.Page = local
		if expected != *child || known.references != 0 || known.height >= 64 {
			return facts, pagedview.ErrRange
		}
		if err := decoder.workspace.reference(ctx, local); err != nil {
			return facts, err
		}
		if err := facts.add(*child, known.last, known.parent); err != nil {
			return facts, err
		}
		facts.height = max(facts.height, known.height+1)
		child.Page = known.branch.Page
	}
	return facts, nil
}

func (facts *structuralCheckpointPage) add(branch pagedview.Branch, last, parent string) error {
	if branch.Key == "" || last < branch.Key || facts.last != "" && facts.last >= branch.Key || facts.parent != "" && facts.parent != parent {
		return pagedview.ErrRange
	}
	if branch.Weight < 0 || branch.Weight > math.MaxInt64-facts.branch.Weight || branch.Count < 0 || branch.Count > math.MaxInt64-facts.branch.Count || branch.Unresolved < 0 || branch.Unresolved > math.MaxInt64-facts.branch.Unresolved {
		return pagedview.ErrWeight
	}
	if facts.branch.Key == "" {
		facts.branch.Key = branch.Key
	}
	facts.last, facts.parent = last, parent
	facts.branch.Weight += branch.Weight
	facts.branch.Count += branch.Count
	facts.branch.Unresolved += branch.Unresolved
	facts.branch.Fingerprint = facts.branch.Fingerprint.Combine(branch.Fingerprint)
	facts.branch.BaselineFingerprint = facts.branch.BaselineFingerprint.Combine(branch.BaselineFingerprint)
	return nil
}

func (decoder *structuralCheckpointDecoder) directory(ctx context.Context, body []byte) error {
	var record structuralCheckpointDirectory
	if err := decodeStructuralJSON(body, &record); err != nil {
		return err
	}
	observation := record.Observation
	observation.Path = string(record.Path)
	if record.Observation.Path != "" || !validStructuralPath(observation.Path) || observation.Sequence < 0 || observation.FirstListed < 0 || observation.Entries < 0 {
		return pagedview.ErrRange
	}
	if observation.FirstListed > observation.Sequence || observation.Complete && observation.FirstListed == 0 || record.Page != 0 && observation.FirstListed == 0 {
		return pagedview.ErrRange
	}
	if decoder.mark != nil {
		observation.Invalidation = decoder.mark(observation.Path)
	}
	if decoder.epoch.BootID != "" {
		observation.Epoch = decoder.epoch
	}
	if decoder.directories > 0 && decoder.lastDirectory >= observation.Path {
		return pagedview.ErrRange
	}
	if record.Page != 0 {
		local := record.Page
		page, err := decoder.workspace.fact(ctx, local)
		if err != nil || page.references != 0 || page.parent != "" && page.parent != observation.Path {
			return pagedview.ErrRange
		}
		if err := decoder.workspace.reference(ctx, local); err != nil {
			return err
		}
		record.Page = page.branch.Page
	}
	directory := structuralDirectory{page: record.Page, observation: observation}
	if err := decoder.builder.directories.Set(ctx, observation.Path, directory); err != nil {
		return err
	}
	decoder.directories++
	decoder.lastDirectory = observation.Path
	return nil
}

func validStructuralPath(value string) bool {
	return value != "" && !strings.ContainsRune(value, 0) && value != ".." && !strings.HasPrefix(value, "../") && !strings.HasPrefix(value, "/") && path.Clean(value) == value
}

func validateStructuralDirectoryItems(ctx context.Context, builder *structuralBuilder) error {
	return builder.directories.Visit(ctx, func(directory structuralDirectory) error {
		index := pagedview.RangeIndex[TreeItem]{Store: builder, Root: directory.page}
		after := ""
		for {
			items, err := index.ReadAfter(ctx, after, indexBatchSize)
			if err != nil || len(items) == 0 {
				return err
			}
			for _, item := range items {
				node := indexNode{path: item.Value.Path, name: path.Base(item.Value.Path), isDir: directoryOrderKind(item.Key), isSymlink: item.Value.Symlink}
				want, err := builder.item(ctx, node)
				if err != nil {
					return err
				}
				want.Value.Sequence = item.Value.Sequence
				if item != want || item.Value.Sequence > directory.observation.Sequence {
					return pagedview.ErrRange
				}
			}
			after = items[len(items)-1].Key
		}
	})
}
