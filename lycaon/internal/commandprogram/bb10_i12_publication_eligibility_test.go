package commandprogram

import (
	"context"
	"slices"
	"testing"
)

// TestBB10I12PublicationFollowsProducerEligibility verifies BB10 I12 across
// direct, grouped, and nested producers with replace (>) and append (>>)
// destinations, both pipe-status policies, cancellation, typed refusal, early
// consumer closure, and an eligible control with identical stdout and status.
func TestBB10I12PublicationFollowsProducerEligibility(t *testing.T) {
	forms := []struct {
		name string
		wrap func(producer, op string) string
	}{
		{"direct", func(producer, op string) string { return producer + " " + op + " out" }},
		{"grouped", func(producer, op string) string { return "{ " + producer + "; } " + op + " out" }},
		{"nested", func(producer, op string) string { return "(" + producer + ") " + op + " out" }},
	}

	t.Run("complete_production_publishes_replace_and_append", func(t *testing.T) {
		for _, form := range forms {
			for _, op := range []string{">", ">>"} {
				t.Run(form.name+"_"+op, func(t *testing.T) {
					f := newEvalFixture(t)
					f.publication.published = map[string]string{"out": "seed\n"}
					script := "echo early > prior; " + form.wrap("echo fresh | cat", op)
					out, err := f.run(t, t.Context(), script)
					if err != nil || out.Stop != nil || out.Status != 0 {
						t.Fatalf("outcome=%+v err=%v", out, err)
					}
					want := "fresh\n"
					if op == ">>" {
						want = "seed\nfresh\n"
					}
					if f.publication.published["out"] != want || f.publication.published["prior"] != "early\n" {
						t.Fatalf("published=%v, want out=%q prior=early\\n", f.publication.published, want)
					}
					if slices.Contains(f.publication.abandoned, "out") {
						t.Fatalf("eligible destination abandoned: %v", f.publication.abandoned)
					}
				})
			}
		}
	})

	t.Run("failed_upstream_blocks_publication_under_both_pipefail_policies", func(t *testing.T) {
		for _, policy := range []struct {
			name, prefix string
		}{
			{"default_pipe_policy", ""},
			{"pipefail_enabled", "set -o pipefail; "},
		} {
			for _, form := range forms {
				for _, op := range []string{">", ">>"} {
					t.Run(policy.name+"_"+form.name+"_"+op, func(t *testing.T) {
						f := newEvalFixture(t)
						f.publication.published = map[string]string{"out": "seed\n"}
						script := policy.prefix + "echo early > prior; " + form.wrap("fail 1 | cat", op)
						out, err := f.run(t, t.Context(), script)
						if err != nil || out.Stop != nil || out.Status != 1 {
							t.Fatalf("outcome=%+v err=%v, want status 1 without stop", out, err)
						}
						if f.publication.published["out"] != "seed\n" {
							t.Fatalf("out=%q, want preserved seed\\n", f.publication.published["out"])
						}
						if f.publication.published["prior"] != "early\n" {
							t.Fatalf("earlier committed effect lost: %v", f.publication.published)
						}
						if !slices.Contains(f.publication.abandoned, "out") {
							t.Fatalf("ineligible destination not abandoned: %v", f.publication.abandoned)
						}
						var pipeIneligible bool
						for _, child := range out.Children {
							if child.Command == "|" && !child.Result.PublicationEligible {
								pipeIneligible = true
							}
						}
						if !pipeIneligible {
							t.Fatalf("expected ineligible pipeline settlement in %+v", out.Children)
						}
					})
				}
			}
		}
	})

	t.Run("valid_early_consumer_closure_publishes", func(t *testing.T) {
		for _, op := range []string{">", ">>"} {
			t.Run("replace_or_append_"+op, func(t *testing.T) {
				f := newEvalFixture(t)
				f.publication.published = map[string]string{"out": "seed\n"}
				script := "cat <<EOF | head -n 1 " + op + " out\none\ntwo\nthree\nEOF\n"
				out, err := f.run(t, t.Context(), script)
				if err != nil || out.Stop != nil || out.Status != 0 {
					t.Fatalf("outcome=%+v err=%v", out, err)
				}
				want := "one\n"
				if op == ">>" {
					want = "seed\none\n"
				}
				if f.publication.published["out"] != want {
					t.Fatalf("out=%q, want %q", f.publication.published["out"], want)
				}
				if len(f.publication.abandoned) != 0 {
					t.Fatalf("unexpected abandoned destinations: %v", f.publication.abandoned)
				}
			})
		}
	})

	t.Run("cancellation_and_typed_refusal_block_publication_and_preserve_prior", func(t *testing.T) {
		t.Run("canceled_producer", func(t *testing.T) {
			f := newEvalFixture(t)
			f.publication.published = map[string]string{"out": "seed\n"}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			out, _ := f.run(t, ctx, "{ echo canceled | cat; } > out")
			if f.publication.published["out"] != "seed\n" {
				t.Fatalf("canceled producer published out=%q (outcome=%+v)", f.publication.published["out"], out)
			}
		})

		t.Run("typed_refusal_producer", func(t *testing.T) {
			f := newEvalFixture(t)
			f.publication.published = map[string]string{"out": "seed\n"}
			out, err := f.run(t, t.Context(), "echo early > prior; { cat --unknown-i12-flag | cat; } > out")
			if err == nil && out.Stop == nil {
				t.Fatalf("expected typed refusal, got outcome=%+v", out)
			}
			if f.publication.published["out"] != "seed\n" {
				t.Fatalf("refused producer mutated out=%q", f.publication.published["out"])
			}
			if f.publication.published["prior"] != "early\n" {
				t.Fatalf("earlier committed effect lost: %v", f.publication.published)
			}
		})
	})

	t.Run("eligible_control_matches_visible_stdout_and_status_but_differs_in_eligibility", func(t *testing.T) {
		// Both commands exit 1 with empty visible stdout, yet the comparison
		// pipeline (echo alpha | grep zzz) is publication-eligible while
		// (fail 1 | cat) is ineligible: the oracle must inspect eligibility.
		eligibleFix := newEvalFixture(t)
		eligibleFix.publication.published = map[string]string{"out": "seed\n"}
		eligibleOut, err := eligibleFix.run(t, t.Context(), "echo alpha | grep zzz > out")
		if err != nil || eligibleOut.Stop != nil || eligibleOut.Status != 1 {
			t.Fatalf("eligible control: outcome=%+v err=%v", eligibleOut, err)
		}

		ineligibleFix := newEvalFixture(t)
		ineligibleFix.publication.published = map[string]string{"out": "seed\n"}
		ineligibleOut, err := ineligibleFix.run(t, t.Context(), "fail 1 | cat > out")
		if err != nil || ineligibleOut.Stop != nil || ineligibleOut.Status != 1 {
			t.Fatalf("ineligible case: outcome=%+v err=%v", ineligibleOut, err)
		}

		if eligibleFix.stdout.String() != ineligibleFix.stdout.String() || eligibleOut.Status != ineligibleOut.Status {
			t.Fatalf("control mismatch: stdout=(%q,%q) status=(%d,%d)",
				eligibleFix.stdout.String(), ineligibleFix.stdout.String(), eligibleOut.Status, ineligibleOut.Status)
		}
		if eligibleFix.publication.published["out"] != "" || slices.Contains(eligibleFix.publication.abandoned, "out") {
			t.Fatalf("eligible control did not publish empty result: pub=%v abandoned=%v", eligibleFix.publication.published, eligibleFix.publication.abandoned)
		}
		if ineligibleFix.publication.published["out"] != "seed\n" || !slices.Contains(ineligibleFix.publication.abandoned, "out") {
			t.Fatalf("ineligible case did not preserve seed: pub=%v abandoned=%v", ineligibleFix.publication.published, ineligibleFix.publication.abandoned)
		}
	})
}
