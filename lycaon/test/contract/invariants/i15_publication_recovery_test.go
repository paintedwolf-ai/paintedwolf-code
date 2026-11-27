package invariants_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/forbiddencalls"
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
	r := rule(t, forbiddencalls.Family, publicationRecoveryRuleName)

	for name, tc := range map[string]struct {
		files          map[string]string
		want           outcome
		wantViolations int
		wantSubstring  string
	}{
		"clean retained-output recovery": {
			files: cleanPublicationRecoveryTree,
			want:  passes,
		},
		"seeded direct program execution": {
			files: with(cleanPublicationRecoveryTree, map[string]string{
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
			want:           violates,
			wantViolations: 1,
			wantSubstring:  "ProgramRuntime.Run",
		},
		"seeded direct evaluator field call": {
			files: with(cleanPublicationRecoveryTree, map[string]string{
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
			want:           violates,
			wantViolations: 1,
			wantSubstring:  "ProgramRuntime.Evaluate",
		},
		"seeded direct process launch": {
			files: with(cleanPublicationRecoveryTree, map[string]string{
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
			want:           violates,
			wantViolations: 1,
			wantSubstring:  "os/exec",
		},
		"seeded transitive execution through helper": {
			files: with(cleanPublicationRecoveryTree, map[string]string{
				"lycaon/internal/tools/native/command/runtime.go": `package command

import "context"

type ProgramRuntime struct {
	Evaluate func(context.Context) error
}

func (r *ProgramRuntime) Run(ctx context.Context) error { return nil }

func (r *ProgramRuntime) publishRetained(ctx context.Context) error { return r.Run(ctx) }
`,
			}),
			want:           violates,
			wantViolations: 1,
			wantSubstring:  "ProgramRuntime.Run",
		},
		"renamed anchor fails closed": {
			files: with(cleanPublicationRecoveryTree, map[string]string{
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
			want: missesAnchor,
		},
		"empty corpus fails closed": {
			files: nil,
			want:  fails,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := invariants.Evaluate(fixture(t, tc.files), r)
			expect(t, res, tc.want, tc.wantViolations)
			if tc.wantSubstring != "" && (len(res.Violations) == 0 || !strings.Contains(res.Violations[0], tc.wantSubstring)) {
				t.Fatalf("expected violation mentioning %q, got %v", tc.wantSubstring, res.Violations)
			}
		})
	}
}
