package sourcecomparison

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

const currentIndexBytes = 32
const currentMemoryBytes = 2 << 20

// CurrentDocument keeps immutable text and row coordinates on disk. Neither
// source length nor line count creates a resident text or coordinate array.
type CurrentDocument struct {
	Summary      api.SourceComparisonSummary
	RawSHA256    string
	text, index  *os.File
	bytes        int64
	memory, disk *pagedview.Reservation
}

func NewCurrent(ctx context.Context, input io.Reader, encoding, path string, memory, disk *pagedview.Budget) (*CurrentDocument, error) {
	d := &CurrentDocument{}
	var err error
	d.memory, err = memory.ReserveEvictingIdle(currentMemoryBytes)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			d.Close()
		}
	}()
	d.disk, err = disk.Reserve(0)
	if err != nil {
		return nil, err
	}
	d.text, err = currentSnapshotFile()
	if err != nil {
		return nil, err
	}
	d.index, err = currentSnapshotFile()
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	reader, err := textfile.NewRuneReader(io.TeeReader(input, hash), encoding)
	if err != nil {
		return nil, err
	}
	if err := d.capture(ctx, reader); err != nil {
		return nil, err
	}
	d.RawSHA256 = hex.EncodeToString(hash.Sum(nil))
	side := api.SourceReaderSide{Path: path, SHA256: d.RawSHA256, Lines: d.Summary.After.Lines, Availability: "available"}
	d.Summary.Before, d.Summary.After = side, side
	d.Summary.ChangeAreas = []api.SourceReaderChangeArea{}
	ok = true
	return d, nil
}

type currentCoordinate struct{ offset, length, line, column int64 }

func (d *CurrentDocument) capture(ctx context.Context, reader *textfile.RuneReader) error {
	text, index := bufio.NewWriterSize(d.text, 64<<10), bufio.NewWriterSize(d.index, 64<<10)
	fragment := make([]byte, 0, 4096)
	line, column := int64(1), int64(0)
	flush := func() error {
		if len(fragment) == 0 {
			return nil
		}
		size := d.bytes + int64(len(fragment)) + int64(d.Summary.Rows+1)*currentIndexBytes
		if err := d.disk.Resize(size); err != nil {
			return err
		}
		var record [currentIndexBytes]byte
		for n, value := range []int64{d.bytes, int64(len(fragment)), line, column} {
			if value < 0 {
				return pagedview.ErrRange
			}
			binary.LittleEndian.PutUint64(record[n*8:], uint64(value))
		}
		if _, err := index.Write(record[:]); err != nil {
			return err
		}
		if _, err := text.Write(fragment); err != nil {
			return err
		}
		d.bytes += int64(len(fragment))
		d.Summary.Rows++
		d.Summary.After.Lines = int(line)
		column += int64(width(string(fragment)))
		fragment = fragment[:0]
		return nil
	}
	previousCR := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		value, _, err := reader.ReadRune()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if value == '\n' && previousCR {
			previousCR = false
			continue
		}
		previousCR = value == '\r'
		if previousCR {
			value = '\n'
		}
		if len(fragment)+utf8.RuneLen(value) > cap(fragment) {
			if err := flush(); err != nil {
				return err
			}
		}
		fragment = utf8.AppendRune(fragment, value)
		if value == '\n' {
			if err := flush(); err != nil {
				return err
			}
			line++
			column = 0
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if err := text.Flush(); err != nil {
		return err
	}
	return index.Flush()
}

func (d *CurrentDocument) Close() {
	for _, file := range []*os.File{d.text, d.index} {
		if file != nil {
			_ = file.Close()
		}
	}
	if d.memory != nil {
		d.memory.Close()
	}
	if d.disk != nil {
		d.disk.Close()
	}
}

func (d *CurrentDocument) coordinate(row int) (currentCoordinate, error) {
	if row < 0 || row >= d.Summary.Rows {
		return currentCoordinate{}, pagedview.ErrRange
	}
	var record [currentIndexBytes]byte
	if _, err := d.index.ReadAt(record[:], int64(row)*currentIndexBytes); err != nil {
		return currentCoordinate{}, err
	}
	var values [4]int64
	for n := range values {
		value := binary.LittleEndian.Uint64(record[n*8:])
		if value > math.MaxInt64 {
			return currentCoordinate{}, pagedview.ErrRange
		}
		values[n] = int64(value)
	}
	coordinate := currentCoordinate{values[0], values[1], values[2], values[3]}
	if coordinate.length < 1 || coordinate.length > 4096 || coordinate.offset > d.bytes-coordinate.length ||
		coordinate.line < 1 || coordinate.line > int64(d.Summary.After.Lines) {
		return currentCoordinate{}, pagedview.ErrRange
	}
	return coordinate, nil
}

func (d *CurrentDocument) row(row int) (api.SourceReaderRow, error) {
	coordinate, err := d.coordinate(row)
	if err != nil {
		return api.SourceReaderRow{}, err
	}
	text := make([]byte, coordinate.length)
	if _, err := d.text.ReadAt(text, coordinate.offset); err != nil {
		return api.SourceReaderRow{}, err
	}
	return api.SourceReaderRow{Index: row, End: row + 1, Kind: "equal", Text: string(text), BeforeLine: int(coordinate.line), AfterLine: int(coordinate.line), Column: int(coordinate.column), Changed: []api.SourceReaderSpan{}}, nil
}

func (d *CurrentDocument) Extent(context.Context) (int64, error) { return int64(d.Summary.Rows), nil }

func (d *CurrentDocument) Locate(ctx context.Context, row int) (int64, ComparisonAnchor, error) {
	if err := ctx.Err(); err != nil {
		return 0, ComparisonAnchor{}, err
	}
	if row < 0 || row >= d.Summary.Rows {
		return 0, ComparisonAnchor{}, pagedview.ErrRange
	}
	return int64(row), ComparisonAnchor{Row: row}, nil
}

func (d *CurrentDocument) RowAtLine(ctx context.Context, line int) (int, error) {
	line = max(1, min(line, d.Summary.After.Lines))
	low, high := 0, d.Summary.Rows
	for low < high {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		middle := low + (high-low)/2
		coordinate, err := d.coordinate(middle)
		if err != nil {
			return 0, err
		}
		if coordinate.line < int64(line) {
			low = middle + 1
		} else {
			high = middle
		}
	}
	return low, nil
}

func (d *CurrentDocument) Frame(ctx context.Context, offset int64, limit int) ([]ProjectedRow, int64, error) {
	if offset < 0 || offset > int64(d.Summary.Rows) || limit < 1 || limit > pagedview.MaxRows {
		return nil, 0, pagedview.ErrRange
	}
	rows := make([]ProjectedRow, 0, limit)
	for rank := offset; rank < int64(d.Summary.Rows) && len(rows) < limit; rank++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		row, err := d.row(int(rank))
		if err != nil {
			return nil, 0, err
		}
		rows = append(rows, ProjectedRow{Rank: rank, Source: row})
	}
	return rows, int64(d.Summary.Rows), nil
}
