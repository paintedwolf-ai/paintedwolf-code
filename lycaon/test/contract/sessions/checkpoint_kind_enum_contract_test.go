package contract

import "github.com/lycaon/lycaon/test/contract/internal/wirespec"

import contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"

import "testing"

func TestCheckpointKindEnumSync(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	goEnums, err := wirespec.DiscoverAPIStringEnums(root)
	contractcheck.FailErr(t, "discover API string enums in pkg/api", err)
	want := []string{"tool_approval", "content_apply"}
	contractcheck.FailSetEqual(t, "CheckpointKind enum", want, goEnums["CheckpointKind"])
}
