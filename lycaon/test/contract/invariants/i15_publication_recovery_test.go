package invariants_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/forbiddencalls"
	"github.com/lycaon/lycaon/test/contract/invariants/invariantstest"
)

const publicationRecoveryRuleName = "publication-recovery-never-reexecutes-or-launches"

var cleanPublicationRecoveryTree = map[string]string{
	"lycaon/internal/tools/native/command/runtime.go": `package command

import "context"

type ProgramRuntime struct {
	Evaluate func(context.Context) error
}

func (r *ProgramRuntime) Run(ctx context.Context) error { return nil }

func (r *ProgramRuntime) publishRetained(ctx context.Context) error { return nil }
`,
	"lycaon/internal/tools/native/command/program_publication_recovery.go": `package command

import "context"

func (r *ProgramRuntime) RecoverProgramPublications(ctx context.Context) error {
	return r.recoverProgramPublication(ctx)
}

func (r *ProgramRuntime) recoverProgramPublication(ctx context.Context) error {
	return r.materializeProgramOutputs(ctx)
}

func (r *ProgramRuntime) materializeProgramOutputs(ctx context.Context) error {
	return r.publishRetained(ctx)
}
`,
}

// I15 AST invariant: publication recovery stays isolated from ordinary program
// execution and process-launch entry points, and fails closed on drift.
func TestPublicationRecoveryASTInvariant(t *testing.T) {
	t.Parallel()
	r := invariantstest.Rule(t, forbiddencalls.Family, publicationRecoveryRuleName)

	for name, tc := range map[string]struct {
		files          map[string]string
		want           invariantstest.Outcome
		wantViolations int
		wantSubstring  string
	}{
		"clean retained-output recovery": {
			files: cleanPublicationRecoveryTree,
			want:  invariantstest.Passes,
		},
		"seeded direct program execution": {
			files: invariantstest.With(cleanPublicationRecoveryTree, map[string]string{
				"lycaon/internal/tools/native/command/program_publication_recovery.go": `package command

import "context"

func (r *ProgramRuntime) RecoverProgramPublications(ctx context.Context) error {
	return r.recoverProgramPublication(ctx)
}

func (r *ProgramRuntime) recoverProgramPublication(ctx context.Context) error {
	if err := r.Run(ctx); err != nil {
		return err
	}
	return r.materializeProgramOutputs(ctx)
}

func (r *ProgramRuntime) materializeProgramOutputs(ctx context.Context) error {
	return r.publishRetained(ctx)
}
`,
			}),
			want:           invariantstest.Violates,
			wantViolations: 1,
			wantSubstring:  "ProgramRuntime.Run",
		},
		"seeded direct evaluator field call": {
			files: invariantstest.With(cleanPublicationRecoveryTree, map[string]string{
				"lycaon/internal/tools/native/command/program_publication_recovery.go": `package command

import "context"

func (r *ProgramRuntime) RecoverProgramPublications(ctx context.Context) error {
	return r.recoverProgramPublication(ctx)
}

func (r *ProgramRuntime) recoverProgramPublication(ctx context.Context) error {
	if err := r.Evaluate(ctx); err != nil {
		return err
	}
	return r.materializeProgramOutputs(ctx)
}

func (r *ProgramRuntime) materializeProgramOutputs(ctx context.Context) error {
	return r.publishRetained(ctx)
}
`,
			}),
			want:           invariantstest.Violates,
			wantViolations: 1,
			wantSubstring:  "ProgramRuntime.Evaluate",
		},
		"seeded direct process launch": {
			files: invariantstest.With(cleanPublicationRecoveryTree, map[string]string{
				"lycaon/internal/tools/native/command/program_publication_recovery.go": `package command

import (
	"context"
	"os/exec"
)

func (r *ProgramRuntime) RecoverProgramPublications(ctx context.Context) error {
	return r.recoverProgramPublication(ctx)
}

func (r *ProgramRuntime) recoverProgramPublication(ctx context.Context) error {
	return r.materializeProgramOutputs(ctx)
}

func (r *ProgramRuntime) materializeProgramOutputs(ctx context.Context) error {
	_ = exec.Command("true")
	return r.publishRetained(ctx)
}
`,
			}),
			want:           invariantstest.Violates,
			wantViolations: 1,
			wantSubstring:  "os/exec",
		},
		"seeded transitive execution through helper": {
			files: invariantstest.With(cleanPublicationRecoveryTree, map[string]string{
				"lycaon/internal/tools/native/command/runtime.go": `package command

import "context"

type ProgramRuntime struct {
	Evaluate func(context.Context) error
}

func (r *ProgramRuntime) Run(ctx context.Context) error { return nil }

func (r *ProgramRuntime) publishRetained(ctx context.Context) error { return r.Run(ctx) }
`,
			}),
			want:           invariantstest.Violates,
			wantViolations: 1,
			wantSubstring:  "ProgramRuntime.materializeProgramOutputs reaches command execution entrypoint ProgramRuntime.Run via ProgramRuntime.publishRetained",
		},
		"seeded transitive process launch through helper": {
			files: invariantstest.With(cleanPublicationRecoveryTree, map[string]string{
				"lycaon/internal/tools/native/command/runtime.go": `package command

import (
	"context"
	"os/exec"
)

type ProgramRuntime struct {
	Evaluate func(context.Context) error
}

func (r *ProgramRuntime) Run(ctx context.Context) error { return nil }

func (r *ProgramRuntime) publishRetained(ctx context.Context) error {
	_ = exec.Command("true")
	return nil
}
`,
			}),
			want:           invariantstest.Violates,
			wantViolations: 1,
			wantSubstring:  "ProgramRuntime.materializeProgramOutputs reaches os/exec process launch Command via ProgramRuntime.publishRetained",
		},
		"renamed anchor fails closed": {
			files: invariantstest.With(cleanPublicationRecoveryTree, map[string]string{
				"lycaon/internal/tools/native/command/program_publication_recovery.go": `package command

import "context"

func (r *ProgramRuntime) RecoverPublicationsRenamed(ctx context.Context) error {
	return r.recoverProgramPublication(ctx)
}

func (r *ProgramRuntime) recoverProgramPublication(ctx context.Context) error {
	return r.materializeProgramOutputs(ctx)
}

func (r *ProgramRuntime) materializeProgramOutputs(ctx context.Context) error {
	return r.publishRetained(ctx)
}
`,
			}),
			want: invariantstest.MissesAnchor,
		},
		"empty corpus fails closed": {
			files: nil,
			want:  invariantstest.Fails,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := invariants.Evaluate(invariantstest.Fixture(t, tc.files), r)
			invariantstest.Expect(t, res, tc.want, tc.wantViolations)
			if tc.wantSubstring != "" && (len(res.Violations) == 0 || !strings.Contains(res.Violations[0], tc.wantSubstring)) {
				t.Fatalf("expected violation mentioning %q, got %v", tc.wantSubstring, res.Violations)
			}
		})
	}
}
