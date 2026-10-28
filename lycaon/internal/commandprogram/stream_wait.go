package commandprogram

import "io"

type pipeProducer struct {
	pipe *BytePipe
	wait *ExecutionWait
}

func (p pipeProducer) Write(b []byte) (int, error) { return p.pipe.write(b, p.wait) }

// streamsForWait binds inherited endpoints to the activity that now uses them.
// The rendezvous reports only actual blocking, after checking readiness.
func streamsForWait(streams Streams, activity *ExecutionWait) Streams {
	for fd := range streams.readers {
		if in, ok := streams.Reader(fd).(pipeConsumer); ok {
			in.wait = activity
			streams.SetReader(fd, in)
		}
		var out io.Writer = streams.Writer(fd)
		switch endpoint := out.(type) {
		case *BytePipe:
			streams.SetWriter(fd, pipeProducer{pipe: endpoint, wait: activity})
		case pipeProducer:
			endpoint.wait = activity
			streams.SetWriter(fd, endpoint)
		}
	}
	return streams
}
