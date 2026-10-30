package textutil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
	"github.com/lycaon/lycaon/internal/tools/native/textutil/sedscript"
)

// openWrites stages every w file before any input is read, as GNU sed opens
// them; each publishes complete output when the run succeeds.
func (r *sedRun) openWrites() (map[string]io.Writer, error) {
	writers := map[string]io.Writer{}
	for _, name := range r.program.WriteFiles() {
		stage, err := r.inv.openDestination(commandprogram.Field{Value: name, Span: r.word.Span})
		if err != nil {
			return nil, err
		}
		r.stages[name] = stage
		writers[name] = nativeutility.NewOutput(r.inv.ctx, stage, r.inv.limit("stream_bytes"))
	}
	return writers, nil
}

// settleWrites publishes every w file, or abandons them all on failure.
func (r *sedRun) settleWrites(err error) error {
	for _, name := range r.program.WriteFiles() {
		if stage, ok := r.stages[name]; ok {
			err = settleStage(r.inv.ctx, stage, err)
		}
	}
	return err
}

// readFiles captures each r and R file once, pinned like any input; a file
// that is missing or unreadable reads as nothing, as in GNU sed.
func (r *sedRun) readFiles() (map[string][]byte, error) {
	files := map[string][]byte{}
	if len(r.program.ReadFiles()) == 0 {
		return files, nil
	}
	paths, err := newPaths(r.inv)
	if err != nil {
		return nil, err
	}
	for _, name := range r.program.ReadFiles() {
		word := commandprogram.Field{Value: name, Span: r.word.Span}
		reader, _, err := r.inv.capture(paths, word)
		if err != nil {
			if _, ok := ordinary(err); ok {
				continue
			}
			return nil, pathRejection(err, word)
		}
		content, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			return nil, err
		}
		files[name] = content
	}
	return files, nil
}

// inPlace edits each file separately: line numbers, $ and ranges restart per
// file while hold space carries over. Each file's destination is staged,
// then its source pinned, and its complete output published (after any
// backup) before the next file starts. An unreadable file is reported and
// skipped; any other failure stops the run and earlier files stay
// published. q ends the run after publishing the current file's output.
func (r *sedRun) inPlace(files commandprogram.Fields, suffix string) error {
	inv := r.inv
	if len(files) == 0 {
		return inv.usage(inv.commandWord(), "a PATH to edit in place")
	}
	paths, err := newPaths(inv)
	if err != nil {
		return err
	}
	for _, operand := range files {
		if operand.Value == "-" {
			return inv.usage(operand, "a file; standard input cannot be edited in place")
		}
		// The destination is pinned first, so a change between the two
		// captures conflicts at publication instead of being overwritten.
		stage, err := inv.openDestination(operand)
		if err != nil {
			return err
		}
		reader, err := captureExisting(inv, paths, operand)
		if err != nil || reader == nil {
			if abandon := stage.Abandon(context.WithoutCancel(inv.ctx)); err != nil || abandon != nil {
				return errors.Join(err, abandon)
			}
			continue
		}
		quit, err := r.editFile(operand, reader, stage, suffix)
		if err != nil || quit {
			return err
		}
	}
	return nil
}

func (r *sedRun) editFile(operand commandprogram.Field, reader io.ReadCloser, stage commandprogram.StagedOutput, suffix string) (bool, error) {
	inv := r.inv
	defer func() { _ = reader.Close() }()
	var backup commandprogram.StagedOutput
	var err error
	if suffix != "" {
		var original []byte
		if original, err = io.ReadAll(reader); err == nil {
			err = inv.reserve(int64(len(original)))
		}
		if err == nil {
			backup, err = inv.openDestination(commandprogram.Field{Value: backupName(operand.Value, suffix), Span: operand.Span})
		}
		if err == nil {
			_, err = nativeutility.NewOutput(inv.ctx, backup, inv.limit("stream_bytes")).Write(original)
		}
		reader = io.NopCloser(bytes.NewReader(original))
	}
	quit := false
	if err == nil {
		out := nativeutility.NewOutput(inv.ctx, stage, inv.limit("stream_bytes"))
		quit, err = r.machine.Stream(r.input([]source{{name: operand.Value, reader: reader}}), out)
		err = r.failure(err)
	}
	if backup != nil {
		err = settleStage(inv.ctx, backup, err)
	}
	return quit, settleStage(inv.ctx, stage, err)
}

// backupName follows GNU sed: SUFFIX is appended, or each * in it is the
// file's base name, placed beside the file unless it names a directory.
func backupName(file, suffix string) string {
	if !strings.Contains(suffix, "*") {
		return file + suffix
	}
	name := strings.ReplaceAll(suffix, "*", path.Base(file))
	if strings.Contains(name, "/") {
		return name
	}
	if dir := path.Dir(file); dir != "." {
		return dir + "/" + name
	}
	return name
}

// sedInput yields the records of each source in order.
type sedInput struct {
	inv       *invocation
	sources   []source
	delimiter byte
	records   *records
}

func (r *sedRun) input(sources []source) *sedInput {
	delimiter := byte('\n')
	if r.inv.flag("-z") {
		delimiter = 0
	}
	return &sedInput{inv: r.inv, sources: sources, delimiter: delimiter}
}

func (s *sedInput) Next() (sedscript.Record, error) {
	for len(s.sources) > 0 {
		if s.records == nil {
			records, err := s.inv.records(s.sources[0].reader, s.delimiter)
			if err != nil {
				return sedscript.Record{}, err
			}
			s.records = records
		}
		text, terminated, err := s.records.nextTerminated()
		if errors.Is(err, io.EOF) {
			s.records, s.sources = nil, s.sources[1:]
			continue
		}
		if err != nil {
			return sedscript.Record{}, err
		}
		return sedscript.Record{Text: text, Terminated: terminated, Source: s.sources[0].name}, nil
	}
	return sedscript.Record{}, io.EOF
}
