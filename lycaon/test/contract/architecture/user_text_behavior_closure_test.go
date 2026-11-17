package contract

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

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

// Every boolean gate in a coordinator template is a fact the host declares:
// a reader fact from the production vocabulary, or one of the occurrence
// values the coordinator render sites supply.
func TestCoordinatorPromptBooleanGatesRegistered(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	vocab, err := prompts.BuildReaderFactVocabulary(nil)
	contractcheck.FailErr(t, "build reader-fact vocabulary", err)
	gates, err := scanCoordinatorPongoBooleanGates(filepath.Join(root, "lycaon", "config", "packs"))
	contractcheck.FailErr(t, "scan coordinator pongo boolean gates", err)
	if gates.files == 0 || len(gates.sites) == 0 {
		t.Fatalf("scanned %d coordinator templates and %d gates; the scan selects nothing", gates.files, len(gates.sites))
	}
	var violations []string
	usedOccurrence := map[string]bool{}
	for _, site := range gates.sites {
		switch {
		case bannedCoordinatorGateNameRE.MatchString(site.gate):
			violations = append(violations, site.rel+": forbidden NL-style prompt gate "+site.gate)
		case vocab.AllowsReference(site.gate):
		case coordinatorOccurrenceGates[site.gate]:
			usedOccurrence[site.gate] = true
		default:
			violations = append(violations, site.rel+": undeclared coordinator boolean gate "+site.gate)
		}
	}
	for gate := range coordinatorOccurrenceGates {
		if !usedOccurrence[gate] {
			violations = append(violations, "coordinatorOccurrenceGates entry "+gate+" gates no coordinator template")
		}
	}
	contractcheck.FailViolations(t, "coordinator prompt gate closure violations", violations)
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
