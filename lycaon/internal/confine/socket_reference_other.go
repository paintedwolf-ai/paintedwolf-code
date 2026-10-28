//go:build !linux

package confine

import "os"

func socketCommandFiles([]SocketGrant) ([]*os.File, func(), error) { return nil, func() {}, nil }

func appendSocketCommandFiles(extra, _ []*os.File) ([]*os.File, error) { return extra, nil }
