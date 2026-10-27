package configlayout

import "os"

// NixRetention is one live indirect root. A desktop can independently hold a
// shared lock on LockPath so a dead helper cannot make its root reclaimable.
type NixRetention struct {
	LockPath string
	lock     *os.File
	release  func()
}

func (retention *NixRetention) Close() {
	if retention != nil && retention.release != nil {
		retention.release()
	}
}
