package backup

import "os"

// RecoveryCaptureUsage describes how the kernel materialized a new snapshot.
// Shared payload is its logical size; later copy-on-write changes are separate.
type RecoveryCaptureUsage struct {
	DatabaseBytes      int64
	PayloadCopiedBytes int64
	PayloadSharedBytes int64
	CopiedFiles        int
	SharedFiles        int
}

func (u *RecoveryCaptureUsage) addFile(path string, shared bool) error {
	if u == nil {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if shared {
		u.PayloadSharedBytes += info.Size()
		u.SharedFiles++
	} else {
		u.PayloadCopiedBytes += info.Size()
		u.CopiedFiles++
	}
	return nil
}
