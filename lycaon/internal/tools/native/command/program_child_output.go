package command

import (
	"context"
	"errors"
	"io"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

const childOutputBytes = 64 << 10

// childOutputs preserves source coordinates and bounded failed-producer diagnostics.
func (r *programRun) childOutputs(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (commandprogram.Streams, func(context.Context, toolcontract.ProducerResult, error) error, error) {
	var buffers [2]*boundedBuffer
	release := func() {}
	if r.retainOutput != nil {
		var err error
		release, err = r.memory.Reserve(ctx, 4*childOutputBytes)
		if err != nil {
			return streams, nil, err
		}
		for i := range buffers {
			buffers[i] = &boundedBuffer{limit: childOutputBytes, data: make([]byte, 0, childOutputBytes)}
		}
	}
	var views []*childStreamView
	wrap := func(writer io.Writer, buffer *boundedBuffer) io.Writer {
		if view := newChildStreamView(writer); view != nil {
			if buffer != nil {
				view.copy = buffer
			}
			views = append(views, view)
			return view
		}
		if buffer != nil {
			return io.MultiWriter(nonNilWriter(writer), buffer)
		}
		return nonNilWriter(writer)
	}
	streams.Output, streams.Error = wrap(streams.Output, buffers[0]), wrap(streams.Error, buffers[1])
	finish := func(ctx context.Context, result toolcontract.ProducerResult, cause error) error {
		defer release()
		defer func() {
			for _, view := range views {
				view.Close()
			}
		}()
		failed := cause != nil || result.Status != 0 || (result.Terminal != "" && result.Terminal != toolcontract.ProducerCompleted)
		if !failed || r.retainOutput == nil {
			return nil
		}
		var err error
		for i, name := range []string{"stdout", "stderr"} {
			buffer := buffers[i]
			if data := buffer.Bytes(); len(data) > 0 {
				err = errors.Join(err, r.retainOutput(context.WithoutCancel(ctx), request.Invocation.Identity.ExecutionID, name, data, result.Complete && !buffer.truncated))
			}
		}
		return err
	}
	return streams, finish, nil
}
