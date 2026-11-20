package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openWatch counts opens of files and directories under a tree, from the
// kernel, whichever process opens them.
type openWatch struct {
	file     *os.File
	opens    atomic.Int64
	overflow atomic.Bool
	done     chan struct{}
}

func watchOpens(root string) (*openWatch, error) {
	// A nonblocking descriptor joins the runtime poller, so Close ends a pending read.
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	// Walking opens each directory, so every watch is added after the walk.
	var directories []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			directories = append(directories, path)
		}
		return err
	})
	for _, directory := range directories {
		if err != nil {
			break
		}
		_, err = unix.InotifyAddWatch(fd, directory, unix.IN_OPEN)
	}
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	w := &openWatch{file: os.NewFile(uintptr(fd), "inotify"), done: make(chan struct{})}
	go w.read()
	return w, nil
}

func (w *openWatch) read() {
	defer close(w.done)
	buffer := make([]byte, 64<<10)
	for {
		n, err := w.file.Read(buffer)
		if err != nil || n <= 0 {
			return
		}
		for offset := 0; offset+unix.SizeofInotifyEvent <= n; {
			event := (*unix.InotifyEvent)(unsafe.Pointer(&buffer[offset])) // #nosec G103 -- kernel event layout
			switch {
			case event.Mask&unix.IN_Q_OVERFLOW != 0:
				w.overflow.Store(true)
			case event.Mask&unix.IN_OPEN != 0:
				w.opens.Add(1)
			}
			offset += unix.SizeofInotifyEvent + int(event.Len)
		}
	}
}

// Count is the opens seen so far; an overflowed queue lost some and reads -1.
func (w *openWatch) Count() int64 {
	if w.overflow.Load() {
		return -1
	}
	return w.opens.Load()
}

func (w *openWatch) Close() error {
	err := w.file.Close()
	<-w.done
	return err
}
