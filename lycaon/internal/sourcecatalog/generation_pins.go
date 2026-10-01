package sourcecatalog

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/pagedview"
)

type generationPin struct {
	count int
	value *structuralGeneration
}

// generationPins is guarded by indexStore.mu.
type generationPins struct {
	held    map[int64]*generationPin
	drained bool
}

type GenerationPin struct {
	store      *indexStore
	Generation int64
	value      *structuralGeneration
	once       sync.Once
	released   atomic.Bool
}

func (p *GenerationPin) clone() (*GenerationPin, error) {
	if p == nil || p.store == nil || p.released.Load() {
		return nil, pagedview.ErrExpired
	}
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	if p.released.Load() {
		return nil, pagedview.ErrExpired
	}
	held := p.store.pins.held[p.Generation]
	if held == nil || held.count <= 0 || held.value != p.value {
		return nil, pagedview.ErrExpired
	}
	held.count++
	return &GenerationPin{store: p.store, Generation: p.Generation, value: p.value}, nil
}

func (s *indexStore) retainGeneration(generation int64, head bool) (*GenerationPin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pins.drained || s.navigation.retired {
		return nil, pagedview.ErrExpired
	}
	s.initializeStructureLocked()
	return s.retainGenerationLocked(generation, head)
}

func (s *indexStore) retainGenerationLocked(generation int64, head bool) (*GenerationPin, error) {
	if head {
		generation = s.structure.id
	}
	pin := s.pins.held[generation]
	if pin == nil {
		value := s.structure
		if generation != value.id {
			if s.completed == nil || generation != s.completed.id {
				return nil, pagedview.ErrRevision
			}
			value = s.completed
		}
		if value == nil {
			return nil, pagedview.ErrRevision
		}
		if value != s.structure {
			for _, segment := range value.segments {
				segment.retain()
			}
		}
		pin = &generationPin{value: value}
		s.pins.held[generation] = pin
	}
	pin.count++
	return &GenerationPin{store: s, Generation: generation, value: pin.value}, nil
}

// RetainedBytes is what this pin keeps alive that head does not: zero for a
// reader on head, and for a superseded generation the segments only it names.
func (p *GenerationPin) RetainedBytes() int64 {
	if p == nil || p.store == nil || p.released.Load() {
		return 0
	}
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	if p.released.Load() || p.value == nil || p.value == p.store.structure {
		return 0
	}
	var total int64
	for id, segment := range p.value.segments {
		if p.store.structure != nil {
			if _, shared := p.store.structure.segments[id]; shared {
				continue
			}
		}
		total += segment.bytes.Bytes()
	}
	return total
}

func (s *indexStore) retainCompletedGeneration() (*GenerationPin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pins.drained || s.navigation.retired {
		return nil, pagedview.ErrExpired
	}
	s.initializeStructureLocked()
	if s.completed == nil {
		return nil, pagedview.ErrMissing
	}
	return s.retainGenerationLocked(s.completed.id, false)
}

func (s *indexStore) initializeStructureLocked() {
	if s.structure == nil {
		s.structure = &structuralGeneration{segments: make(map[uint32]*structuralSegment)}
	}
	if s.pins.held == nil {
		s.pins.held = make(map[int64]*generationPin)
	}
}

func (p *GenerationPin) Release() {
	p.once.Do(func() {
		p.released.Store(true)
		p.store.mu.Lock()
		pin := p.store.pins.held[p.Generation]
		if pin != nil {
			pin.count--
		}
		if pin != nil && pin.count == 0 {
			delete(p.store.pins.held, p.Generation)
			if pin.value != p.store.structure || p.store.pins.drained {
				if pin.value == p.store.structure {
					p.store.structure = nil
				}
				pin.value.close()
			}
		}
		p.store.mu.Unlock()
	})
}

// The caller validates its captured head and invalidations under this same lock.
func (s *indexStore) installStructureLocked(next *structuralGeneration) {
	previous := s.structure
	s.structure = next
	if next.complete {
		for _, segment := range next.segments {
			segment.retain()
		}
		completed := s.completed
		s.completed = next
		if completed != nil {
			completed.close()
		}
	}
	if previous != nil && s.pins.held[previous.id] == nil {
		previous.close()
	}
}

func (s *indexStore) structurePublished(ctx context.Context) {
	s.catalog.navigationChanged(s.root)
	s.scheduleStructuralCheckpoint(ctx)
}
