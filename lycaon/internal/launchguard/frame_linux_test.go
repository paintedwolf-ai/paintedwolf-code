//go:build linux

package launchguard

import "testing"

func TestEntryFrameRequiresRetainedAuthority(t *testing.T) {
	for _, generation := range []*Generation{nil, {}} {
		file, err := CreateEntryFrame(generation, Identity{UID: 1000, GID: 1000}, nil, nil, false)
		if err == nil {
			_ = file.Close()
			t.Fatal("created entry authority without retained installed generation")
		}
	}
}
