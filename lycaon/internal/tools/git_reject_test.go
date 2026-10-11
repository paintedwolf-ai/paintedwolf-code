package tools_test

import (
	"github.com/lycaon/lycaon/internal/toolrejection"

	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/gitexec"
)

func TestGitSerializedFailuresKeepPolicyIdentity(t *testing.T) {
	for _, tc := range []struct {
		cause error
		code  string
	}{
		{&gitexec.UnsafeRepoConfigError{Keys: []string{"merge.custom.driver"}}, "GIT_REPO_CONFIG_UNSAFE"},
		{&gitexec.SigningUnsupportedError{Detail: "signer unavailable"}, "GIT_SIGNING_UNSUPPORTED"},
		{&gitengine.UnavailableError{Reason: "missing"}, "GIT_ENGINE_UNAVAILABLE"},
	} {
		out, err := git.MarshalToolFailure(fmt.Errorf("git operation: %w", tc.cause))
		if out == "" || !errors.Is(err, tc.cause) {
			t.Fatalf("failure became successful or lost diagnostics: %q %v", out, err)
		}
		reject := toolrejection.GitFailureObservation(err)
		if reject == nil || reject.Code != tc.code {
			t.Fatalf("typed failure lost its OAR identity: %#v", reject)
		}
		if tc.code == "GIT_REPO_CONFIG_UNSAFE" && !reflect.DeepEqual(reject.Data["keys"], []string{"merge.custom.driver"}) {
			t.Fatalf("unsafe keys lost: %#v", reject.Data)
		}
	}
	if reject := toolrejection.GitFailureObservation(errors.New("GIT_REPO_CONFIG_UNSAFE")); reject != nil {
		t.Fatal("ordinary error text became host authority")
	}
}
