package exec

import (
	"context"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/fseffect"
)

// stageFiles holds the redirection files of one running group. Output commits
// when the group exits, so a later group reads what an earlier one wrote.
type stageFiles struct {
	spec   *RedirectSpec
	writes map[string]*os.File
	// order commits files in the order the plan names them.
	order   []stageWrite
	readers []*os.File
}

type stageWrite struct {
	path   string
	target OutputTarget
}

func openStageFiles(spec *RedirectSpec, stages []Stage) (*stageFiles, error) {
	files := &stageFiles{spec: spec, writes: map[string]*os.File{}}
	for _, stage := range stages {
		for _, sink := range []argv.Sink{stage.Streams.StdoutSink(), stage.Streams.StderrSink()} {
			if sink.Kind != argv.SinkFile {
				continue
			}
			if _, open := files.writes[sink.Path]; open {
				continue
			}
			loc, err := spec.location(sink.Path)
			if err != nil {
				files.close()
				return nil, err
			}
			target := OutputTarget{Location: loc, Append: sink.Append}
			file, err := openOutputFile(spec, target)
			if err != nil {
				files.close()
				return nil, err
			}
			files.writes[sink.Path] = file
			files.order = append(files.order, stageWrite{path: sink.Path, target: target})
		}
	}
	return files, nil
}

func (f *stageFiles) writer(path string) io.Writer {
	return f.writes[path]
}

// openStdin opens a stage's stdin file for the life of the group.
func (f *stageFiles) openStdin(path string) (*os.File, error) {
	loc, err := f.spec.location(path)
	if err != nil {
		return nil, err
	}
	file, err := fseffect.OpenRead(loc)
	if err != nil {
		return nil, err
	}
	f.readers = append(f.readers, file)
	return file, nil
}

func (f *stageFiles) commit(ctx context.Context) error {
	if f == nil || f.spec == nil || f.spec.Commit == nil {
		return nil
	}
	for _, write := range f.order {
		if err := commitOutputFile(ctx, f.spec, f.writes[write.path], write.target); err != nil {
			return err
		}
	}
	return nil
}

func (f *stageFiles) close() {
	if f == nil {
		return
	}
	for _, write := range f.order {
		closeOutputFile(f.spec, f.writes[write.path])
	}
	for _, reader := range f.readers {
		_ = reader.Close()
	}
	f.writes, f.order, f.readers = nil, nil, nil
}
