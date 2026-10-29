package textutil

import (
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
		writers[name] = nativeutility.NewOutput(r.inv.ctx, stage, "stream_bytes", r.inv.limit("stream_bytes"))
	}
	return writers, nil
}

// settleWrites publishes every w file, or abandons them all on failure.
func (r *sedRun) settleWrites(err error) error {
	for _, name := range r.program.WriteFiles() {
		if stage, ok := r.stages[name]; ok {
			err = r.inv.settleStage(stage, err)
		}
	}
	return err
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

func (r *sedRun) editFile(operand commandprogram.Field, reader io.ReadCloser, stage commandprogram.StagedOutput, suffix string) (quit bool, finalErr error) {
	inv := r.inv
	originalReader := reader
	defer func() {
		if closeErr := originalReader.Close(); closeErr != nil {
			if finalErr == nil {
				finalErr = closeErr
			} else {
				finalErr = errors.Join(finalErr, closeErr)
			}
		}
	}()
	var backup *sedBackup
	var err error
	if suffix != "" {
		backup, err = r.prepareBackup(operand, reader, suffix)
		if backup != nil {
			defer func() {
				if closeErr := backup.Close(); closeErr != nil {
					if finalErr == nil {
						finalErr = closeErr
					} else {
						finalErr = errors.Join(finalErr, closeErr)
					}
				}
			}()
			reader = backup.replay
		}
	}
	quit = false
	if err == nil {
		out := nativeutility.NewOutput(inv.ctx, stage, "stream_bytes", inv.limit("stream_bytes"))
		input := r.input([]source{{name: operand.Value, reader: reader}})
		quit, err = r.machine.Stream(input, out)
		input.Close()
		err = r.failure(err)
	}
	if backup != nil && backup.stage != nil {
		err = inv.settleStage(backup.stage, err)
	}
	return quit, inv.settleStage(stage, err)
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
			_ = s.sources[0].reader.Close()
			s.records.Close()
			s.records, s.sources = nil, s.sources[1:]
			continue
		}
		if err != nil {
			return sedscript.Record{}, err
		}
		record := sedscript.Record{Text: text, Terminated: terminated, Source: s.sources[0].name, Offset: s.records.offset}
		if operand, ok := s.sources[0].reader.(*operandReader); ok {
			record.Pin = operand.pin
		}
		return record, nil
	}
	return sedscript.Record{}, io.EOF
}

func (s *sedInput) Close() {
	if s.records != nil {
		s.records.Close()
		s.records = nil
	}
}
