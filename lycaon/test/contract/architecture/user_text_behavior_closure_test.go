package contract

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPromptPathKickHooksRegistered(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	scan, err := scanUserTextBehaviorClosure(filepath.Join(root, "lycaon"))
	contractcheck.FailErr(t, "scan user-text behavior closure", err)
	if len(scan.UnregisteredPromptKickHooks) > 0 {
		t.Fatalf("unregistered maybeQueue*Kick hooks on Prompt path (register in registeredPromptPathKickHooks with trigger class):\n%s",
			strings.Join(scan.UnregisteredPromptKickHooks, "\n"))
	}
}

func TestNoUserTextNLProbeToBehaviorSink(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	scan, err := scanUserTextBehaviorClosure(filepath.Join(root, "lycaon"))
	contractcheck.FailErr(t, "scan user-text behavior closure", err)
	if len(scan.NLProbeToSinkViolations) > 0 {
		t.Fatalf("user-text string probe must not reach behavior sinks:\n%s",
			strings.Join(scan.NLProbeToSinkViolations, "\n"))
	}
}

func TestNoErrErrorStringParsingInCoordinatorPackages(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	scan, err := scanUserTextBehaviorClosure(filepath.Join(root, "lycaon"))
	contractcheck.FailErr(t, "scan user-text behavior closure", err)
	if len(scan.ErrTextParseViolations) > 0 {
		t.Fatalf("forbidden err.Error() string parsing in session/coordinator/workflow production code:\n%s",
			strings.Join(scan.ErrTextParseViolations, "\n"))
	}
}

func TestCoordinatorPromptBooleanGatesRegistered(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	catalogRoot := filepath.Join(root, "lycaon", "config", "packs")
	violations, err := scanCoordinatorPongoBooleanGates(catalogRoot)
	contractcheck.FailErr(t, "scan coordinator pongo boolean gates", err)
	if len(violations) > 0 {
		contractcheck.FailViolations(t, "coordinator prompt gate closure violations", violations)
	}
}

func TestKickContractTriggerClassesNotUserText(t *testing.T) {
	t.Parallel()
	reg := catalogfixture.LoadInformBindings(t)
	var violations []string
	for _, b := range reg.AllInform() {
		if b == nil || !b.IsInform() || !strings.HasPrefix(b.Render, "coordinator-") {
			continue
		}
		id := catalogfixture.BindingShortID(b.Render)
		class := strings.TrimSpace(b.Invariants.TriggerClass)
		if class == "" {
			violations = append(violations, id+": missing invariants.trigger_class")
			continue
		}
		if !allowedKickTriggerClasses[class] {
			violations = append(violations, id+": forbidden trigger_class "+class)
		}
		firesText := strings.ToLower(strings.Join(b.Invariants.FiresIn, " "))
		for _, banned := range forbiddenKickFiresInSubstrings {
			if strings.Contains(firesText, banned) {
				violations = append(violations, id+": fires_in must not reference NL intent ("+banned+")")
			}
		}
	}
	if len(violations) > 0 {
		contractcheck.FailViolations(t, "kick trigger_class Binding invariant violations", violations)
	}
}
