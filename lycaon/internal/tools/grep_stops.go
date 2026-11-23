package tools

import "sync"

// GrepStoppedFile is one input of a multi-file grep whose search stopped on a
// record budget. LinesEmitted counts the lines it printed before the stop.
type GrepStoppedFile struct {
	Path         string
	Line         int
	RecordBytes  int64
	Resource     string
	LinesEmitted int
}

// GrepSearch is one multi-file grep that stopped at least one input.
type GrepSearch struct {
	Stopped        []GrepStoppedFile
	SearchedFiles  int
	MatchedFiles   int
	PatternLiteral bool
}

// GrepStops collects a program's grep searches that stopped inputs.
type GrepStops struct {
	mu       sync.Mutex
	searches []GrepSearch
}

// Add records one search; a nil collector drops it.
func (s *GrepStops) Add(search GrepSearch) {
	if s == nil || len(search.Stopped) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.searches = append(s.searches, search)
}

// Searches returns the recorded searches in completion order.
func (s *GrepStops) Searches() []GrepSearch {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]GrepSearch(nil), s.searches...)
}
