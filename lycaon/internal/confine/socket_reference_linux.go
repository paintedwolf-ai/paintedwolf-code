//go:build linux

package confine

import (
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
)

func socketCommandFiles(grants []SocketGrant) ([]*os.File, func(), error) {
	files := make([]*os.File, 0, len(grants))
	closeFiles := func() {
		for _, file := range files {
			_ = file.Close()
		}
	}
	for _, grant := range grants {
		lease, err := grant.Binding.Acquire()
		if err != nil {
			closeFiles()
			return nil, func() {}, err
		}
		file, err := lease.Duplicate()
		lease.Close()
		if err != nil {
			closeFiles()
			return nil, func() {}, err
		}
		files = append(files, file)
	}
	return files, closeFiles, nil
}

func appendSocketCommandFiles(extra []*os.File, files []*os.File) ([]*os.File, error) {
	if len(files) == 0 {
		return extra, nil
	}
	if len(extra)+helperProfileFD > linuxsandbox.SocketReferenceFD {
		return nil, fmt.Errorf("socket reference descriptors overlap helper control")
	}
	for len(extra)+helperProfileFD < linuxsandbox.SocketReferenceFD {
		extra = append(extra, nil)
	}
	return append(extra, files...), nil
}
