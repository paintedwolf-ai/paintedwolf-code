package commandprogram

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

type typedPublishFailurePublication struct {
	base   *fakePublication
	target string
	err    error
}

type typedPublishFailureStage struct {
	StagedOutput
	err error
}

func (p typedPublishFailurePublication) Open(ctx context.Context, destination OutputDestination) (StagedOutput, error) {
	staged, err := p.base.Open(ctx, destination)
	if err != nil {
		return nil, err
	}
	if destination.Target.Value == p.target {
		return typedPublishFailureStage{StagedOutput: staged, err: p.err}, nil
	}
	return staged, nil
}

func (s typedPublishFailureStage) Publish(context.Context) (OutputPublished, error) {
	return OutputPublished{}, s.err
}

func TestPipelinePublicationFenceCoreCases(t *testing.T) {
	t.Run("upstream_nonzero_downstream_success_preserves_destination", func(t *testing.T) {
		f := newEvalFixture(t)
		f.publication.published = map[string]string{"out": "old\n"}
		out, err := f.run(t, t.Context(), "fail 1 | cat > out")
		if err != nil || out.Stop != nil || out.Status != 1 {
			t.Fatalf("outcome = %+v, err = %v, want status 1 without host stop", out, err)
		}
		if f.publication.published["out"] != "old\n" {
			t.Fatalf("out = %q, want old\\n", f.publication.published["out"])
		}
		if !slices.Contains(f.publication.abandoned, "out") {
			t.Fatalf("abandoned = %v, want out", f.publication.abandoned)
		}
		var sawPipeline, sawFail, sawCat bool
		for _, child := range out.Children {
			switch child.Command {
			case "|":
				sawPipeline = true
				if child.Result.PublicationEligible {
					t.Fatalf("pipeline marked publication eligible: %+v", child)
				}
			case "fail":
				sawFail = true
				if child.Result.Status != 1 || child.Result.PublicationEligible {
					t.Fatalf("upstream fail = %+v, want status 1 ineligible", child)
				}
			case "cat":
				sawCat = true
				if child.Result.Status != 0 {
					t.Fatalf("downstream cat = %+v, want status 0", child)
				}
			}
		}
		if !sawPipeline || !sawFail || !sawCat {
			t.Fatalf("missing child settlements: pipeline=%v fail=%v cat=%v", sawPipeline, sawFail, sawCat)
		}
	})

	t.Run("normal_eligible_redirect_publishes", func(t *testing.T) {
		f := newEvalFixture(t)
		out, err := f.run(t, t.Context(), "echo hello | cat > out")
		if err != nil || out.Stop != nil || out.Status != 0 {
			t.Fatalf("outcome = %+v, err = %v", out, err)
		}
		if f.publication.published["out"] != "hello\n" {
			t.Fatalf("out = %q, want hello\\n", f.publication.published["out"])
		}
	})

	t.Run("native_early_consumer_closure_publishes", func(t *testing.T) {
		f := newEvalFixture(t)
		out, err := f.run(t, t.Context(), "yes | head -n 1 > out")
		if err != nil || out.Stop != nil || out.Status != 0 {
			t.Fatalf("outcome = %+v, err = %v", out, err)
		}
		if f.publication.published["out"] != "y\n" {
			t.Fatalf("out = %q, want y\\n", f.publication.published["out"])
		}
		if len(f.publication.abandoned) != 0 {
			t.Fatalf("unexpected abandoned destinations: %v", f.publication.abandoned)
		}
	})

	t.Run("comparison_exit_one_eligible_publishes", func(t *testing.T) {
		f := newEvalFixture(t)
		f.publication.published = map[string]string{"out": "old\n"}
		out, err := f.run(t, t.Context(), "echo alpha | grep zzz > out")
		if err != nil || out.Stop != nil || out.Status != 1 {
			t.Fatalf("outcome = %+v, err = %v, want status 1", out, err)
		}
		if f.publication.published["out"] != "" {
			t.Fatalf("out = %q, want empty published comparison result", f.publication.published["out"])
		}
		if slices.Contains(f.publication.abandoned, "out") {
			t.Fatalf("comparison redirect unexpectedly abandoned: %v", f.publication.abandoned)
		}
	})
}

func TestPipelinePublicationFenceFailureModes(t *testing.T) {
	t.Run("ordinary_publish_failure_stops_later_destinations", func(t *testing.T) {
		f := newEvalFixture(t)
		out, err := f.run(t, t.Context(), "echo kept > earlier; echo one | cat > publish-failed | cat > later; echo $?")
		if err != nil || out.Stop != nil {
			t.Fatalf("ordinary publish failure latched stop: out=%+v err=%v", out, err)
		}
		if f.stdout.String() != "1\n" {
			t.Fatalf("pipeline status = %q, want 1\\n", f.stdout.String())
		}
		if f.publication.published["earlier"] != "kept\n" {
			t.Fatalf("earlier = %q, want kept\\n", f.publication.published["earlier"])
		}
		if _, exists := f.publication.published["later"]; exists {
			t.Fatalf("later destination published after earlier failure: %+v", f.publication.published)
		}
		if !slices.Contains(f.publication.abandoned, "later") {
			t.Fatalf("later destination not abandoned: %v", f.publication.abandoned)
		}
	})

	t.Run("typed_publish_refusal_preserves_earlier_and_stops_program", func(t *testing.T) {
		f := newEvalFixture(t)
		refusal := &codedError{"COMMAND_REVISION_CONFLICT"}
		f.ports.Publication = typedPublishFailurePublication{base: f.publication, target: "conflict", err: refusal}
		out, err := f.run(t, t.Context(), "echo kept > earlier; echo one | cat > conflict | cat > later; echo after")
		if !errors.Is(err, refusal) || out.Stop == nil || out.Stop.Result.StopCode != "COMMAND_REVISION_CONFLICT" {
			t.Fatalf("typed refusal = %v, stop = %+v", err, out.Stop)
		}
		if f.publication.published["earlier"] != "kept\n" {
			t.Fatalf("earlier = %q, want kept\\n", f.publication.published["earlier"])
		}
		if _, exists := f.publication.published["later"]; exists {
			t.Fatalf("later destination published after typed refusal: %+v", f.publication.published)
		}
		if !slices.Contains(f.publication.abandoned, "later") {
			t.Fatalf("later destination not abandoned: %v", f.publication.abandoned)
		}
		if f.stdout.String() != "" {
			t.Fatalf("suffix statement ran after typed publish refusal: %q", f.stdout.String())
		}
	})
}

func TestPipelinePublicationFenceNestedGroupsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		script      string
		wantPublish bool
	}{
		{name: "group_stage_upstream_fail", script: "fail 1 | { cat > out; }", wantPublish: false},
		{name: "subshell_stage_upstream_fail", script: "fail 1 | (cat > out)", wantPublish: false},
		{name: "pipeline_in_subshell_upstream_fail", script: "(fail 1 | cat > out)", wantPublish: false},
		{name: "redirect_on_group_wrapping_pipeline", script: "{ fail 1 | cat; } > out", wantPublish: false},
		{name: "nested_pipeline_in_group_outer_fail", script: "fail 1 | { echo ok | cat > out; }", wantPublish: false},
		{name: "group_stage_all_eligible", script: "echo ok | { cat > out; }", wantPublish: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEvalFixture(t)
			f.publication.published = map[string]string{"out": "old\n"}
			out, err := f.run(t, t.Context(), tc.script)
			if err != nil || out.Stop != nil {
				t.Fatalf("script %q: out=%+v err=%v", tc.script, out, err)
			}
			if tc.wantPublish {
				if f.publication.published["out"] != "ok\n" {
					t.Fatalf("script %q: out=%q, want ok\\n", tc.script, f.publication.published["out"])
				}
				return
			}
			if f.publication.published["out"] != "old\n" {
				t.Fatalf("script %q: out=%q, want old\\n", tc.script, f.publication.published["out"])
			}
			if !slices.Contains(f.publication.abandoned, "out") {
				t.Fatalf("script %q: abandoned=%v, want out", tc.script, f.publication.abandoned)
			}
		})
	}

	t.Run("cancellation_abandons_deferred_redirects_without_leak", func(t *testing.T) {
		f := newEvalFixture(t)
		f.publication.published = map[string]string{"out": "old\n"}
		f.log.started = make(chan string, 4)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		var (
			out ProgramOutcome
			err error
		)
		go func() {
			defer close(done)
			out, err = f.run(t, ctx, "sleep 10 | cat > out")
		}()
		for seen := 0; seen < 2; seen++ {
			<-f.log.started
		}
		cancel()
		<-done
		if !errors.Is(err, context.Canceled) && out.Stop == nil {
			t.Fatalf("expected cancellation stop: out=%+v err=%v", out, err)
		}
		if f.publication.published["out"] != "old\n" {
			t.Fatalf("cancelled pipeline overwrote out: %q", f.publication.published["out"])
		}
		if !slices.Contains(f.publication.abandoned, "out") {
			t.Fatalf("cancelled pipeline did not abandon out: %v", f.publication.abandoned)
		}
		if usage := f.ports.Memory.InUse(); usage != (toolcontract.ProgramMemoryUsage{}) {
			t.Fatalf("cancelled pipeline leaked program memory: %+v", usage)
		}
	})
}
