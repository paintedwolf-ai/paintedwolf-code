//go:build linux

package linuxsandbox

import "testing"

func TestMetadataWriteRestorationsFollowPlanGrantsAndMasks(t *testing.T) {
	fs := Filesystem{WriteRoots: []string{"/project", "/project/masked/approved"}, WriteFiles: []string{"/project/masked/exact"}, ReadOnly: []string{"/project/masked"}, Hidden: []string{"/project/hidden"}, Restored: []string{"/project/hidden/readable"}}
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/project", true}, {"/project/file", true}, {"/project/masked", false}, {"/project/masked/file", false},
		{"/project/masked/approved/file", true}, {"/project/masked/exact", true}, {"/project/masked/exact/child", false},
		{"/project/hidden/readable", false}, {"/project/hidden/readable/file", false}, {"/project-other", false}, {"/Project", false}, {"/outside", false},
	} {
		if got := metadataWritable(fs, tc.path); got != tc.want {
			t.Errorf("metadataWritable(%s) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
