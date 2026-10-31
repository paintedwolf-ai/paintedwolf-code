package sedscript

import (
	"errors"
	"io"
)

// ReadFile shares the host's byte capture and record-framing owners. Next
// yields LF records; its text remains valid until the next call.
type ReadFile interface {
	io.ReadCloser
	Next() (Record, error)
}

// readFiles owns reached script streams and their replay/copy storage.
type readFiles struct {
	open    func(string) (ReadFile, error)
	reserve func(int64) error
	cursors map[string]ReadFile
	buffer  []byte
}

func (f *readFiles) start() error {
	if err := f.close(); err != nil {
		return err
	}
	clear(f.cursors)
	return nil
}

// close releases any R streams still open after a stopped cycle.
func (f *readFiles) close() error {
	var result error
	for name, file := range f.cursors {
		if file == nil {
			continue
		}
		if err := file.Close(); err != nil {
			if result == nil {
				result = err
			} else {
				result = errors.Join(result, err)
			}
		}
		f.cursors[name] = nil
	}
	return result
}

func (f *readFiles) append(item appendItem, out *output) error {
	if item.line {
		return f.appendLine(item.file, out)
	}
	file, err := f.open(item.file)
	if err != nil || file == nil {
		return err
	}
	return f.copyFile(file, out)
}

func (f *readFiles) copyFile(file ReadFile, out *output) (finalErr error) {
	defer func() {
		if err := file.Close(); err != nil {
			if finalErr == nil {
				finalErr = err
			} else {
				finalErr = errors.Join(finalErr, err)
			}
		}
	}()
	if f.buffer == nil {
		if f.reserve != nil {
			if err := f.reserve(64 << 10); err != nil {
				return err
			}
		}
		f.buffer = make([]byte, 64<<10)
	}
	if err := out.raw(nil); err != nil {
		return err
	}
	_, err := io.CopyBuffer(out.w, file, f.buffer)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func (f *readFiles) appendLine(name string, out *output) error {
	file, opened := f.cursors[name]
	if !opened {
		var err error
		file, err = f.open(name)
		if err != nil {
			return err
		}
		f.cursors[name] = file
	}
	if file == nil {
		return nil
	}
	record, err := file.Next()
	if errors.Is(err, io.EOF) {
		f.cursors[name] = nil
		return file.Close()
	}
	if err != nil {
		return err
	}
	return out.text(record.Text)
}
