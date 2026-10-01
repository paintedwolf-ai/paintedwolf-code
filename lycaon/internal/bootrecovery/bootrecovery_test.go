package bootrecovery

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func journal(name string, after []string, run func(context.Context) error) Entry {
	return Entry{Name: name, Kind: KindJournal, Phase: PhaseBuild, After: after, Run: run}
}

func ok(_ context.Context) error { return nil }

func TestRunOrdersByDeclaredDependency(t *testing.T) {
	r := New()
	var seen []string
	record := func(name string) func(context.Context) error {
		return func(context.Context) error {
			seen = append(seen, name)
			return nil
		}
	}
	// Registered out of order: order comes from After, not from Register calls.
	testutil.FailErr(t, "register retarget", r.Register(journal("editor-retargets", []string{"source-mutations"}, record("editor-retargets"))))
	testutil.FailErr(t, "register source", r.Register(journal("source-mutations", nil, record("source-mutations"))))

	report, err := r.Run(context.Background(), PhaseBuild)
	testutil.FailErr(t, "run build phase", err)
	if got := report.Degraded(); len(got) != 0 {
		t.Fatalf("expected every entry to run cleanly, got %+v", got)
	}
	if strings.Join(seen, ",") != "source-mutations,editor-retargets" {
		t.Fatalf("recovery ran out of order: %v", seen)
	}
}

func TestRecoveryFailureProducesAnUnsettledReport(t *testing.T) {
	r := New()
	boom := errors.New("stale journal row")
	testutil.FailErr(t, "register failing", r.Register(journal("approvals", nil, func(context.Context) error { return boom })))
	testutil.FailErr(t, "register healthy", r.Register(journal("rewinds", nil, ok)))

	report, err := r.Run(context.Background(), PhaseBuild)
	testutil.FailErr(t, "run build phase", err)
	if len(report.Outcomes) != 2 {
		t.Fatalf("every entry must report an outcome, got %d", len(report.Outcomes))
	}
	degraded := report.Degraded()
	if len(degraded) != 1 || degraded[0].Name != "approvals" {
		t.Fatalf("degraded = %+v, want only approvals", degraded)
	}
	if !errors.Is(degraded[0].Err, boom) {
		t.Fatalf("outcome must carry the cause, got %v", degraded[0].Err)
	}
	var unsettled *UnsettledError
	if !errors.As(report.Err(), &unsettled) {
		t.Fatalf("report.Err() = %v, want UnsettledError", report.Err())
	}
	if len(unsettled.Outcomes) != 1 || unsettled.Outcomes[0].Name != "approvals" {
		t.Fatalf("unsettled outcomes = %+v", unsettled.Outcomes)
	}
}

func TestDependentSkipsWhenDependencyDegrades(t *testing.T) {
	r := New()
	ran := false
	testutil.FailErr(t, "register source", r.Register(journal("source-mutations", nil, func(context.Context) error {
		return errors.New("diverged")
	})))
	testutil.FailErr(t, "register retarget", r.Register(journal("editor-retargets", []string{"source-mutations"}, func(context.Context) error {
		ran = true
		return nil
	})))

	report, err := r.Run(context.Background(), PhaseBuild)
	testutil.FailErr(t, "run build phase", err)
	if ran {
		t.Fatal("a dependent must not run on state its dependency failed to settle")
	}
	var retarget Outcome
	for _, o := range report.Outcomes {
		if o.Name == "editor-retargets" {
			retarget = o
		}
	}
	if !retarget.Skipped() {
		t.Fatal("expected editor-retargets to report as skipped")
	}
	if len(retarget.Blocked) != 1 || retarget.Blocked[0] != "source-mutations" {
		t.Fatalf("skip must name the blocking dependency, got %v", retarget.Blocked)
	}
}

func TestReconcileEntriesRegisterAlongsideJournals(t *testing.T) {
	r := New()
	testutil.FailErr(t, "register journal", r.Register(journal("source-mutations", nil, ok)))
	testutil.FailErr(t, "register reconcile", r.Register(Entry{
		Name: "artifact-blobs", Kind: KindReconcile, Phase: PhaseBuild,
		After: []string{"source-mutations"}, Run: ok,
	}))

	report, err := r.Run(context.Background(), PhaseBuild)
	testutil.FailErr(t, "run build phase", err)
	if got := report.Degraded(); len(got) != 0 {
		t.Fatalf("expected every entry to run cleanly, got %+v", got)
	}
	var kinds []string
	for _, o := range report.Outcomes {
		kinds = append(kinds, string(o.Kind))
	}
	if strings.Join(kinds, ",") != "journal,reconcile" {
		t.Fatalf("both shapes must run in one pass, got %v", kinds)
	}
}

func TestPhasesRunSeparately(t *testing.T) {
	r := New()
	var seen []string
	testutil.FailErr(t, "register build", r.Register(Entry{
		Name: "source-mutations", Kind: KindJournal, Phase: PhaseBuild,
		Run: func(context.Context) error { seen = append(seen, "build"); return nil },
	}))
	testutil.FailErr(t, "register serve", r.Register(Entry{
		Name: "prompt-submissions", Kind: KindJournal, Phase: PhaseServe,
		After: []string{"source-mutations"},
		Run:   func(context.Context) error { seen = append(seen, "serve"); return nil },
	}))

	buildReport, err := r.Run(context.Background(), PhaseBuild)
	testutil.FailErr(t, "run build phase", err)
	if len(buildReport.Outcomes) != 1 || buildReport.Outcomes[0].Name != "source-mutations" {
		t.Fatalf("build phase ran the wrong entries: %+v", buildReport.Outcomes)
	}
	serveReport, err := r.Run(context.Background(), PhaseServe)
	testutil.FailErr(t, "run serve phase", err)
	if len(serveReport.Outcomes) != 1 || serveReport.Outcomes[0].Name != "prompt-submissions" {
		t.Fatalf("serve phase ran the wrong entries: %+v", serveReport.Outcomes)
	}
	if strings.Join(seen, ",") != "build,serve" {
		t.Fatalf("phase order = %v", seen)
	}
}

func TestStructuralFaultsAreBuildDefects(t *testing.T) {
	t.Run("unknown dependency", func(t *testing.T) {
		r := New()
		testutil.FailErr(t, "register", r.Register(journal("a", []string{"nobody"}, ok)))
		if _, err := r.Run(context.Background(), PhaseBuild); err == nil {
			t.Fatal("an unregistered After edge must fail the build")
		}
	})
	t.Run("cycle", func(t *testing.T) {
		r := New()
		testutil.FailErr(t, "register a", r.Register(journal("a", []string{"b"}, ok)))
		testutil.FailErr(t, "register b", r.Register(journal("b", []string{"a"}, ok)))
		_, err := r.Run(context.Background(), PhaseBuild)
		if err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Fatalf("expected a cycle error, got %v", err)
		}
	})
	t.Run("build depending on serve", func(t *testing.T) {
		r := New()
		testutil.FailErr(t, "register serve", r.Register(Entry{
			Name: "late", Kind: KindJournal, Phase: PhaseServe, Run: ok,
		}))
		testutil.FailErr(t, "register build", r.Register(Entry{
			Name: "early", Kind: KindJournal, Phase: PhaseBuild, After: []string{"late"}, Run: ok,
		}))
		if _, err := r.Run(context.Background(), PhaseBuild); err == nil {
			t.Fatal("a build entry cannot depend on a serve entry")
		}
	})
	t.Run("duplicate registration", func(t *testing.T) {
		r := New()
		testutil.FailErr(t, "register once", r.Register(journal("a", nil, ok)))
		if err := r.Register(journal("a", nil, ok)); err == nil {
			t.Fatal("duplicate names must be rejected")
		}
	})
	t.Run("missing kind or phase", func(t *testing.T) {
		r := New()
		if err := r.Register(Entry{Name: "a", Run: ok}); err == nil {
			t.Fatal("an entry without a kind must be rejected")
		}
		if err := r.Register(Entry{Name: "a", Kind: KindJournal, Run: ok}); err == nil {
			t.Fatal("an entry without a phase must be rejected")
		}
	})
}
