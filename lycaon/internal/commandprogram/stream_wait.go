package commandprogram

import (
	"context"
	"io"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

type pipeProducer struct {
	pipe *BytePipe
	wait *ExecutionWait
}

func (p pipeProducer) Write(b []byte) (int, error) {
	n, _, err := p.pipe.write(b, p.wait)
	return n, err
}

func (p pipeProducer) WriteLocated(b []byte) (int, int64, error) { return p.pipe.write(b, p.wait) }

func (p pipeProducer) RecordSourceMapping(m toolcontract.StreamSourceMapping) error {
	return p.pipe.RecordSourceMapping(m)
}

func (p pipeProducer) MappingAdmission() (context.Context, *toolcontract.ProgramMemory) {
	return p.pipe.MappingAdmission()
}

// WithActivity binds inherited endpoints to the activity that now uses them.
// The rendezvous reports only actual blocking, after checking readiness.
func (streams Streams) WithActivity(activity *ExecutionWait) Streams {
	for fd := range streams.readers {
		if in, ok := streams.Reader(fd).(pipeConsumer); ok {
			in.wait = activity
			streams.SetReader(fd, in)
		}
		out := streams.Writer(fd)
		switch endpoint := out.(type) {
		case *BytePipe:
			streams.SetWriter(fd, pipeProducer{pipe: endpoint, wait: activity})
		case pipeProducer:
			endpoint.wait = activity
			streams.SetWriter(fd, endpoint)
		case interface {
			WithActivity(*ExecutionWait) io.Writer
		}:
			streams.SetWriter(fd, endpoint.WithActivity(activity))
		}
	}
	return streams
}
