//go:build unix

package toolcommand

import (
	"os"
	"reflect"
	"strconv"
	"testing"
)

func TestExactCommandReplacementMapsOnlyExplicitCurrentOwnership(t *testing.T) {
	uid := strconv.Itoa(os.Getuid())
	gid := strconv.Itoa(os.Getgid())
	command := "chown " + uid + ":" + gid + " scripts/run.sh"
	want := []ReplacementCall{{Tool: "chown", Args: map[string]any{
		"owner": uid, "group": gid, "paths": []any{"scripts/run.sh"},
	}}}
	got, ok := ExactCommandReplacement(t.Context(), command, t.TempDir(), "")
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement = %#v ok=%v, want %#v", got, ok, want)
	}
}
