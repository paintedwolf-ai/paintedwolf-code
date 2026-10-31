package command

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// programStream stages a top-level stream privately until its screened snapshot
// enters the existing output retention lifecycle. Pipe bytes never enter it.
type programStream struct {
	mu        sync.Mutex
	limit     int64
	size      int64
	spool     *scratch.ByteSpool
	releases  []func()
	held      int64
	admission tooloutput.SpillAdmission
	ctx       context.Context
	truncated bool
	mappings  *sourceMappingBuffer
}

func newProgramStream(ctx context.Context, limit int64, admission tooloutput.SpillAdmission, memory *toolcontract.ProgramMemory) *programStream {
	return &programStream{ctx: ctx, limit: limit, admission: admission, mappings: newSourceMappingBuffer(ctx, memory)}
}

func (r *ProgramRuntime) newProgramStreams(ctx context.Context, run *programRun) (*programStream, *programStream) {
	var outputLimit int64
	for _, limit := range run.catalog.Language.Limits {
		if limit.ID == "spill_bytes" {
			outputLimit = limit.Maximum
		}
	}
	admission := r.sessionResources(run.tc.Identity.SessionID)
	return newProgramStream(ctx, outputLimit, admission, run.memory), newProgramStream(ctx, outputLimit, admission, run.memory)
}

func (s *programStream) Write(p []byte) (int, error) {
	n, _, err := s.writeLocated(p)
	return n, err
}

func (s *programStream) writeLocated(p []byte) (int, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(p) == 0 {
		return 0, s.size, nil
	}
	if err := s.ctx.Err(); err != nil {
		return 0, s.size, err
	}
	if int64(len(p)) > s.limit-s.size {
		s.truncated = true
		limit := &commandprogram.StreamLimitError{Limit: "spill_bytes", Maximum: s.limit, Observed: s.size + int64(len(p))}
		return 0, s.size, errors.Join(limit, &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": "spill_bytes", "resource_unit": "bytes", "maximum": limit.Maximum, "observed": limit.Observed}})
	}
	target := min(s.limit, ((s.size+int64(len(p))+(1<<20)-1)/(1<<20))*(1<<20))
	if target > s.held {
		release, err := s.admission.ReserveSpill(s.ctx, target-s.held)
		if err != nil {
			s.truncated = true
			var limit *toolcontract.ProgramMemoryLimitError
			if errors.As(err, &limit) {
				err = errors.Join(err, &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": limit.Account, "resource_unit": "bytes", "maximum": limit.Maximum, "observed": limit.Observed}})
			}
			return 0, s.size, err
		}
		s.releases = append(s.releases, release)
		s.held = target
	}
	if s.spool == nil {
		spool, err := scratch.NewByteSpool()
		if err != nil {
			s.truncated = true
			return 0, s.size, err
		}
		s.spool = spool
	}
	start := s.size
	n, err := s.spool.Write(p)
	s.size += int64(n)
	if err != nil {
		s.truncated = true
	}
	return n, start, err
}

func (s *programStream) snapshotInput() (io.Reader, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spool == nil {
		return strings.NewReader(""), 0, nil
	}
	reader, err := s.spool.Reader()
	return reader, s.size, err
}

func (s *programStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.spool != nil {
		err = s.spool.Close()
		s.spool = nil
	}
	for _, release := range s.releases {
		release()
	}
	s.releases = nil
	s.held = 0
	s.mappings.Close()
	return err
}

func (s *programStream) Len() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size
}

func (s *programStream) snapshotMappings(ctx context.Context, length int64) (*commandprogram.MappingLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mappings.Snapshot(ctx, length)
}

func (s *programStream) RecordSourceMapping(m toolcontract.StreamSourceMapping) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mappings.Append(m)
	return nil
}
