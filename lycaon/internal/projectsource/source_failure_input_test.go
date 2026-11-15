package projectsource

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

func TestFailureClassificationPreservesPartialPublication(t *testing.T) {
	root := t.TempDir()
	source, output, missing := filepath.Join(root, "source"), filepath.Join(root, "output"), filepath.Join(root, "missing")
	testutil.FailErr(t, "write original", os.WriteFile(source, []byte("before"), 0600))
	testutil.FailErr(t, "write published output", os.WriteFile(output, []byte("after"), 0600))
	identity, err := fspath.EntryIdentity(source)
	testutil.FailErr(t, "read original identity", err)
	write := sourceMutationPlan{Kind: "write", AbsPath: source, Changed: true, sourceMutationContent: sourceMutationContent{BaseSHA256: textfile.SHA256([]byte("before"))}, sourceMutationPublication: sourceMutationPublication{EffectStarted: true}}
	published := write
	published.AbsPath = output
	for _, tc := range []struct {
		name string
		plan sourceMutationPlan
		safe bool
	}{
		{"unchanged write", write, true},
		{"published write", published, false},
		{"unreadable input", sourceMutationPlan{Kind: "write", AbsPath: missing, Changed: true}, false},
		{"rolled back batch", sourceMutationPlan{Kind: "batch_write", Changed: true, Writes: []sourceMutationPlan{write, write}}, true},
		{"partial batch", sourceMutationPlan{Kind: "batch_write", Changed: true, Writes: []sourceMutationPlan{write, published}}, false},
		{"create absent", sourceMutationPlan{Kind: "create", Changed: true, AbsPath: missing, sourceMutationPublication: sourceMutationPublication{EffectStarted: true}}, true},
		{"create present", sourceMutationPlan{Kind: "create", Changed: true, AbsPath: output, sourceMutationPublication: sourceMutationPublication{EffectStarted: true}}, false},
		{"restore absent", sourceMutationPlan{Kind: "restore", Changed: true, AbsPath: missing, sourceMutationPublication: sourceMutationPublication{EffectStarted: true}}, true},
		{"copy unpublished", sourceMutationPlan{Kind: "copy", Changed: true, ToAbs: missing, sourceMutationPublication: sourceMutationPublication{EffectStarted: true}}, true},
		{"copy published", sourceMutationPlan{Kind: "copy", Changed: true, ToAbs: output, sourceMutationPublication: sourceMutationPublication{EffectStarted: true}}, false},
		{"rename unchanged", sourceMutationPlan{Kind: "rename", Changed: true, FromAbs: source, ToAbs: missing, sourceMutationPublication: sourceMutationPublication{EffectStarted: true, EntryIdentity: identity}}, true},
		{"rename destination present", sourceMutationPlan{Kind: "rename", Changed: true, FromAbs: source, ToAbs: output, sourceMutationPublication: sourceMutationPublication{EffectStarted: true, EntryIdentity: identity}}, false},
		{"rename original missing", sourceMutationPlan{Kind: "rename", Changed: true, FromAbs: missing, ToAbs: output, sourceMutationPublication: sourceMutationPublication{EffectStarted: true, EntryIdentity: identity}}, false},
		{"rename identity changed", sourceMutationPlan{Kind: "rename", Changed: true, FromAbs: source, ToAbs: missing, sourceMutationPublication: sourceMutationPublication{EffectStarted: true, EntryIdentity: "different"}}, false},
		{"rename held", sourceMutationPlan{Kind: "rename", Changed: true, sourceMutationPublication: sourceMutationPublication{EffectStarted: true, HoldStarted: true}}, false},
		{"uninspectable effect", sourceMutationPlan{Kind: "unknown", Changed: true, sourceMutationPublication: sourceMutationPublication{EffectStarted: true}}, false},
		{"unstarted create", sourceMutationPlan{Kind: "create", Changed: true}, true},
		{"no change", sourceMutationPlan{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sourceFailureLeavesInput(&tc.plan); got != tc.safe {
				t.Fatalf("release observation scope = %v, want %v", got, tc.safe)
			}
		})
	}
}

func TestRequireSourceIdentityIgnoresOnlyTheDevice(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	testutil.FailErr(t, "seed first", os.WriteFile(first, nil, 0o600))
	testutil.FailErr(t, "seed second", os.WriteFile(second, nil, 0o600))
	identity, err := fspath.EntryIdentity(first)
	testutil.FailErr(t, "identify first", err)
	testutil.FailErr(t, "moved device", requireSourceIdentity(first, "9"+identity))
	if err := requireSourceIdentity(second, "9"+identity); !errors.Is(err, ErrSourceMutationDiverged) {
		t.Fatalf("another entry = %v", err)
	}
	if err := requireSourceIdentity(first, ""); !errors.Is(err, ErrSourceMutationDiverged) {
		t.Fatalf("no recorded identity = %v", err)
	}
}
