//go:build (!darwin || !cgo) && !linux

package repochange

import (
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
)

type fsnotifyBackendStream struct {
	fw   *fsnotify.Watcher
	lost chan string

	mu     sync.Mutex
	live   map[string]struct{}
	closed bool
}

func newBackendStream() (backendStream, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &fsnotifyBackendStream{
		fw:   fw,
		lost: make(chan string),
		live: map[string]struct{}{},
	}, nil
}

func (s *fsnotifyBackendStream) Add(path string) error {
	path = filepath.Clean(path)
	if err := s.fw.Add(path); err != nil {
		return err
	}
	s.mu.Lock()
	if !s.closed {
		s.live[path] = struct{}{}
	}
	s.mu.Unlock()
	return nil
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
func (s *fsnotifyBackendStream) Lost() <-chan string           { return s.lost }
