//go:build linux

package repochange

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/unix"
)

const inotifyWatchMask = unix.IN_CREATE |
	unix.IN_MODIFY |
	unix.IN_DELETE |
	unix.IN_DELETE_SELF |
	unix.IN_MOVED_TO |
	unix.IN_MOVED_FROM |
	unix.IN_MOVE_SELF |
	unix.IN_ATTRIB

type inotifyStream struct {
	fd     int
	file   *os.File
	events chan fsnotify.Event
	errors chan error
	lost   chan string
	done   chan struct{}

	mu     sync.Mutex
	byPath map[string]uint32
	byWd   map[uint32]string
	closed bool
}

func newBackendStream() (backendStream, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	s := &inotifyStream{
		fd:     fd,
		file:   os.NewFile(uintptr(fd), "inotify"),
		events: make(chan fsnotify.Event),
		errors: make(chan error),
		lost:   make(chan string, 64),
		done:   make(chan struct{}),
		byPath: map[string]uint32{},
		byWd:   map[uint32]string{},
	}
	go s.readEvents()
	return s, nil
}

func (s *inotifyStream) Add(path string) error {
	path = filepath.Clean(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fsnotify.ErrClosed
	}
	wd, err := unix.InotifyAddWatch(s.fd, path, inotifyWatchMask)
	if wd == -1 {
		return err
	}
	uwd := uint32(wd)
	if prev, ok := s.byWd[uwd]; ok && prev != path {
		delete(s.byPath, prev)
	}
	s.byPath[path] = uwd
	s.byWd[uwd] = path
	return nil
}

func (s *inotifyStream) Remove(path string) error {
	path = filepath.Clean(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	wd, ok := s.byPath[path]
	if !ok {
		return fsnotify.ErrNonExistentWatch
	}
	delete(s.byPath, path)
	delete(s.byWd, wd)
	_, err := unix.InotifyRmWatch(s.fd, wd)
	if errors.Is(err, unix.EINVAL) {
		return nil
	}
	return err
}

// Has reports in O(1) with zero allocations whether path holds a live kernel
// inotify watch descriptor on this backend stream.
func (s *inotifyStream) Has(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	_, ok := s.byPath[path]
	return ok
}

func (s *inotifyStream) WatchList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	out := make([]string, 0, len(s.byPath))
	for p := range s.byPath {
		out = append(out, p)
	}
	return out
}

func (s *inotifyStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.byPath = nil
	s.byWd = nil
	s.mu.Unlock()
	err := s.file.Close()
	<-s.done
	return err
}

func (s *inotifyStream) Events() <-chan fsnotify.Event { return s.events }
func (s *inotifyStream) Errors() <-chan error          { return s.errors }
func (s *inotifyStream) Lost() <-chan string           { return s.lost }

func (s *inotifyStream) readEvents() {
	defer func() {
		close(s.done)
		close(s.lost)
		close(s.errors)
		close(s.events)
	}()

	var buf [unix.SizeofInotifyEvent * 4096]byte
	for {
		n, err := s.file.Read(buf[:])
		if err != nil {
			if errors.Is(err, os.ErrClosed) || s.isClosed() {
				return
			}
			if !s.sendError(err) {
				return
			}
			continue
		}
		if n < unix.SizeofInotifyEvent {
			if n == 0 {
				if !s.sendError(io.EOF) {
					return
				}
			}
			continue
		}
		var offset uint32
		for offset <= uint32(n-unix.SizeofInotifyEvent) {
			inEv := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
			if inEv.Mask&unix.IN_Q_OVERFLOW != 0 {
				if !s.sendError(fsnotify.ErrEventOverflow) {
					return
				}
			}
			ev, lostPath, ok := s.handleEvent(inEv, &buf, offset)
			if !ok {
				return
			}
			if lostPath != "" {
				select {
				case s.lost <- lostPath:
				default:
				}
			}
			if ev.Op != 0 {
				if !s.sendEvent(ev) {
					return
				}
			}
			offset += unix.SizeofInotifyEvent + inEv.Len
		}
	}
}

func (s *inotifyStream) handleEvent(inEv *unix.InotifyEvent, buf *[unix.SizeofInotifyEvent * 4096]byte, offset uint32) (fsnotify.Event, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fsnotify.Event{}, "", false
	}
	wd := uint32(inEv.Wd)
	watchPath, ok := s.byWd[wd]
	if !ok {
		return fsnotify.Event{}, "", true
	}
	if inEv.Mask&(unix.IN_UNMOUNT|unix.IN_IGNORED) != 0 {
		delete(s.byWd, wd)
		if s.byPath[watchPath] == wd {
			delete(s.byPath, watchPath)
		}
		if inEv.Mask&unix.IN_UNMOUNT != 0 {
			return fsnotify.Event{}, watchPath, true
		}
		return fsnotify.Event{}, "", true
	}

	name := watchPath
	if inEv.Len > 0 {
		name += "/" + decodeInotifyName(buf, offset, inEv.Len)
	}
	if inEv.Mask&unix.IN_DELETE_SELF != 0 {
		delete(s.byWd, wd)
		if s.byPath[watchPath] == wd {
			delete(s.byPath, watchPath)
		}
		if _, parentWatched := s.byPath[filepath.Dir(watchPath)]; parentWatched {
			return fsnotify.Event{}, "", true
		}
	}
	if inEv.Mask&unix.IN_MOVE_SELF != 0 {
		delete(s.byWd, wd)
		if s.byPath[watchPath] == wd {
			delete(s.byPath, watchPath)
		}
		_, _ = unix.InotifyRmWatch(s.fd, wd)
	}
	return fsnotify.Event{Name: name, Op: translateInotifyMask(inEv.Mask)}, "", true
}

func decodeInotifyName(buf *[unix.SizeofInotifyEvent * 4096]byte, offset, nameLen uint32) string {
	start := int(offset + unix.SizeofInotifyEvent)
	raw := buf[start : start+int(nameLen)]
	end := len(raw)
	for end > 0 && raw[end-1] == 0 {
		end--
	}
	return string(raw[:end])
}

func translateInotifyMask(mask uint32) fsnotify.Op {
	var op fsnotify.Op
	if mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0 {
		op |= fsnotify.Create
	}
	if mask&(unix.IN_DELETE|unix.IN_DELETE_SELF) != 0 {
		op |= fsnotify.Remove
	}
	if mask&unix.IN_MODIFY != 0 {
		op |= fsnotify.Write
	}
	if mask&(unix.IN_MOVED_FROM|unix.IN_MOVE_SELF) != 0 {
		op |= fsnotify.Rename
	}
	if mask&unix.IN_ATTRIB != 0 {
		op |= fsnotify.Chmod
	}
	return op
}

func (s *inotifyStream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *inotifyStream) sendEvent(ev fsnotify.Event) bool {
	if s.isClosed() {
		return false
	}
	s.events <- ev
	return true
}

func (s *inotifyStream) sendError(err error) bool {
	if s.isClosed() {
		return false
	}
	s.errors <- err
	return true
}
