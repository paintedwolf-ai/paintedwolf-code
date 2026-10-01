package exec_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/exec"
)

func TestMergeInlineEnvPerCallOverridesSession(t *testing.T) {
	got := exec.MergeInlineEnv(
		map[string]string{"SESSION": "1", "SHARED": "session"},
		map[string]string{"SHARED": "call", "CALL": "2"},
	)
	if got["SESSION"] != "1" || got["SHARED"] != "call" || got["CALL"] != "2" {
		t.Fatalf("merge = %#v", got)
	}
}
