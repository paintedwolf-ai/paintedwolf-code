//go:build (!darwin || !cgo) && !linux

package repochange

import (
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
)

type fsnotifyBackendStream struct {
	fw   *fsnotify.Watcher
	lost chan watchLoss

	mu        sync.Mutex
	nextRegID uint64
	live      map[string]uint64
	closed    bool
}

func newBackendStream() (backendStream, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &fsnotifyBackendStream{
		fw:   fw,
		lost: make(chan watchLoss),
		live: map[string]uint64{},
	}, nil
}

func (s *fsnotifyBackendStream) Add(path string) (uint64, error) {
	path = filepath.Clean(path)
	if err := s.fw.Add(path); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, fsnotify.ErrClosed
	}
	if existing, ok := s.live[path]; ok {
		return existing, nil
	}
	s.nextRegID++
	s.live[path] = s.nextRegID
	return s.nextRegID, nil
}

func (s *fsnotifyBackendStream) Remove(path string) error {
	path = filepath.Clean(path)
	err := s.fw.Remove(path)
	s.mu.Lock()
	delete(s.live, path)
	s.mu.Unlock()
	return err
}

func (s *fsnotifyBackendStream) Has(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	_, ok := s.live[path]
	return ok
}

func (s *fsnotifyBackendStream) WatchList() []string { return s.fw.WatchList() }

func (s *fsnotifyBackendStream) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.live = nil
		close(s.lost)
	}
	s.mu.Unlock()
	return s.fw.Close()
}

func (s *fsnotifyBackendStream) Events() <-chan fsnotify.Event { return s.fw.Events }
func (s *fsnotifyBackendStream) Errors() <-chan error          { return s.fw.Errors }
func (s *fsnotifyBackendStream) Lost() <-chan watchLoss        { return s.lost }
