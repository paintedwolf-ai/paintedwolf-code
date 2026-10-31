package textutil

import (
	"io"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/tools/native/textutil/sedscript"
)

// readFileFactory binds script operands to the same lazy capture and pin owner
// as positional inputs. GNU sed treats a missing/unreadable r/R file as empty.
func (r *sedRun) readFileFactory() (func(string) (sedscript.ReadFile, error), error) {
	if len(r.program.ReadFiles()) == 0 {
		return nil, nil
	}
	paths, err := newPaths(r.inv)
	if err != nil {
		return nil, err
	}
	return func(name string) (sedscript.ReadFile, error) {
		word := commandprogram.Field{Value: name, Span: r.word.Span}
		reader, _, err := r.inv.capture(paths, word)
		if err != nil {
			if _, ok := ordinary(err); ok {
				return nil, nil
			}
			return nil, pathRejection(err, word)
		}
		return &sedReadFile{inv: r.inv, reader: reader, name: name}, nil
	}, nil
}

// sedReadFile keeps capture and framing storage for one reached script file.
// The machine uses either byte reads for r or LF records for R.
type sedReadFile struct {
	inv     *invocation
	reader  io.ReadCloser
	records *records
	name    string
}

func (f *sedReadFile) Read(p []byte) (int, error) { return f.reader.Read(p) }

func (f *sedReadFile) Next() (sedscript.Record, error) {
	if f.records == nil {
		var err error
		f.records, err = f.inv.records(f.reader, '\n')
		if err != nil {
			return sedscript.Record{}, err
		}
	}
	text, terminated, err := f.records.nextTerminated()
	return sedscript.Record{Text: text, Terminated: terminated, Source: f.name}, err
}

func (f *sedReadFile) Close() error {
	if f.records != nil {
		f.records.Close()
		f.records = nil
	}
	if f.reader == nil {
		return nil
	}
	err := f.reader.Close()
	f.reader = nil
	return err
}
