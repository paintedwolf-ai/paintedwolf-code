//go:build !darwin || !cgo

package repochange

import "github.com/fsnotify/fsnotify"

type fsnotifyPlatformWatcher struct {
	*fsnotify.Watcher
	resync chan struct{}
}

func newPlatformWatcher(string) (platformWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &fsnotifyPlatformWatcher{Watcher: w, resync: make(chan struct{})}, nil
}

func (w *fsnotifyPlatformWatcher) EventChannel() <-chan fsnotify.Event { return w.Events }
func (w *fsnotifyPlatformWatcher) ResyncChannel() <-chan struct{}      { return w.resync }
func (w *fsnotifyPlatformWatcher) ErrorChannel() <-chan error          { return w.Errors }
