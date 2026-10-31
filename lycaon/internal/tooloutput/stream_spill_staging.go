package tooloutput

import (
	"context"
	"fmt"
)

// Encoder stages overlap during publication: plaintext, compressed output,
// then two compressed copies. Admission covers that overlap before each read.
type streamSpillStaging struct {
	ctx               context.Context
	admission         SpillAdmission
	maximum, capacity int64
	// Doubling from 64 KiB to the existing 256 MiB ceiling takes at most 13 leases.
	releases [14]func()
	count    int
}

func newStreamSpillStaging(ctx context.Context, admission SpillAdmission, maximum int64) (*streamSpillStaging, error) {
	s := &streamSpillStaging{ctx: ctx, admission: admission, maximum: maximum}
	capacity := min(maximum, 64<<10)
	release, err := admission.ReserveSpill(ctx, 3*capacity+(1<<20))
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("spill admission returned no release")
	}
	s.capacity, s.releases[0], s.count = capacity, release, 1
	return s, nil
}

func (s *streamSpillStaging) Grow(bytes int64) error {
	if bytes <= s.capacity {
		return nil
	}
	capacity := min(s.maximum, max(bytes, s.capacity*2))
	release, err := s.admission.ReserveSpill(s.ctx, 3*(capacity-s.capacity))
	if err != nil {
		return err
	}
	if release == nil {
		return fmt.Errorf("spill admission returned no release")
	}
	s.capacity = capacity
	s.releases[s.count] = release
	s.count++
	return nil
}

func (s *streamSpillStaging) Close() {
	for i := 0; i < s.count; i++ {
		s.releases[i]()
		s.releases[i] = nil
	}
	s.count = 0
}
