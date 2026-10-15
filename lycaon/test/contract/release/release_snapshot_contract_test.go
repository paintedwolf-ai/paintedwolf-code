package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestSelectedReleaseTagPinsEverySourceCheckout(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string
				With map[string]string
			}
		}
	}
	contractcheck.FailErr(t, "parse release controller", yaml.Unmarshal([]byte(contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), ".github/workflows/release.yml")), &workflow))
	for name, job := range workflow.Jobs {
		count := 0
		for _, step := range job.Steps {
			if !strings.HasPrefix(step.Uses, "actions/checkout@") || step.With["repository"] != "" {
				continue
			}
			if step.With["path"] == ".release-controller" {
				if step.With["ref"] != "refs/heads/main" {
					t.Errorf("%s controller is not pinned to refs/heads/main", name)
				}
				continue
			}
			count++
			if name == "classify" && count == 1 {
				if step.With["ref"] != "refs/heads/main" {
					t.Errorf("classify controller checkout uses %q, want refs/heads/main", step.With["ref"])
				}
				continue
			}
			want := "${{ needs.classify.outputs.source_sha }}"
			if name == "classify" {
				want = "${{ steps.source.outputs.source_sha }}"
			}
			if step.With["ref"] != want {
				t.Errorf("%s checkout %d uses %q, want admitted source %q", name, count, step.With["ref"], want)
			}
		}
	}
}

func TestSelectedReleaseControllerAdmission(t *testing.T) {
	root, repo := contractcheck.RepoRoot(t), t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	run("git", "init", "-b", "main")
	run("git", "config", "user.name", "Release test")
	run("git", "config", "user.email", "release@example.com")
	write := func(path, text string, mode os.FileMode) {
		t.Helper()
		contractcheck.FailErr(t, "write "+path, os.WriteFile(filepath.Join(repo, path), []byte(text), mode))
	}
	write("VERSION", "1.0.0\n", 0o644)
	write("RELEASE_BUILD", "1\n", 0o644)
	run("git", "add", "VERSION", "RELEASE_BUILD")
	run("git", "commit", "-m", "Release snapshot")
	selected := run("git", "rev-parse", "HEAD")
	run("git", "tag", "v1.0.0")
	run("git", "tag", "v9.9.9")
	write("VERSION", "1.0.1\n", 0o644)
	write("RELEASE_BUILD", "2\n", 0o644)
	run("git", "add", "VERSION", "RELEASE_BUILD")
	run("git", "commit", "-m", "Later work")
	later := run("git", "rev-parse", "HEAD")
	run("git", "checkout", "-b", "unlanded")
	write("VERSION", "1.0.2\n", 0o644)
	write("RELEASE_BUILD", "3\n", 0o644)
	run("git", "add", "VERSION", "RELEASE_BUILD")
	run("git", "commit", "-m", "Unlanded work")
	run("git", "tag", "v1.0.2")
	run("git", "checkout", "main")
	run("git", "remote", "add", "origin", repo)
	contractcheck.FailErr(t, "create fixture scripts", os.Mkdir(filepath.Join(repo, "scripts"), 0o755))
	for _, name := range []string{"release-source-validate.sh", "semver-compare.py", "release_semver.py"} {
		data, err := os.ReadFile(filepath.Join(root, "scripts", name))
		contractcheck.FailErr(t, "read "+name, err)
		write("scripts/"+name, string(data), 0o644)
	}
	python, err := exec.LookPath("python3")
	contractcheck.FailErr(t, "find Python", err)
	contractcheck.FailErr(t, "create test executables", os.Mkdir(filepath.Join(repo, "bin"), 0o755))
	// Qualification API evidence is mocked; provenance and workflow shell run unchanged.
	write("bin/python3", "#!/bin/sh\nif [ \"$1\" = scripts/ci_verification.py ]; then\n printf '%s' \"$3\" > \"$QUALIFICATION_RECORD\"\n exit \"$QUALIFICATION_STATUS\"\nfi\nexec \""+python+"\" \"$@\"\n", 0o755)
	admission := ""
	for _, step := range hostedWorkflowFile(t, "release").Jobs["classify"].Steps {
		if strings.Contains(step.Run, "source_sha=") {
			admission = step.Run
			break
		}
	}
	if admission == "" {
		t.Fatal("missing source admission")
	}
	cases := []struct{ name, tag, ref, event, sha, qualification, diagnostic, want string }{
		{"qualified snapshot", "v1.0.0", "refs/heads/main", "workflow_dispatch", later, "0", "", "source_sha=" + selected + "\npublish=true\n"},
		{"dry run", "", "refs/heads/main", "workflow_dispatch", later, "1", "", "source_sha=" + later + "\npublish=false\n"},
		{"wrong controller", "v1.0.0", "refs/heads/feature", "workflow_dispatch", later, "0", "main release controller", ""},
		{"unqualified", "v1.0.0", "refs/heads/main", "workflow_dispatch", later, "1", "", ""},
		{"unlanded", "v1.0.2", "refs/heads/main", "workflow_dispatch", later, "0", "has not landed", ""},
		{"wrong version", "v9.9.9", "refs/heads/main", "workflow_dispatch", later, "0", "does not match VERSION", ""},
		{"missing tag", "v8.0.0", "refs/heads/main", "workflow_dispatch", later, "0", "couldn't find remote ref", ""},
		{"tag push", "", "refs/tags/v1.0.0", "push", selected, "0", "", "source_sha=" + selected + "\npublish=true\n"},
		{"changed tag", "", "refs/tags/v1.0.0", "push", later, "0", "tag changed", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outputFile, record := filepath.Join(t.TempDir(), "output"), filepath.Join(t.TempDir(), "qualification")
			cmd := exec.Command("bash", "-c", admission)
			cmd.Dir = repo
			refType := "branch"
			if tc.event == "push" {
				refType = "tag"
			}
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(repo, "bin")+":"+os.Getenv("PATH"), "REQUESTED_TAG="+tc.tag, "GITHUB_EVENT_NAME="+tc.event, "GITHUB_REF="+tc.ref, "GITHUB_REF_TYPE="+refType, "GITHUB_REF_NAME=v1.0.0", "GITHUB_SHA="+tc.sha, "GITHUB_OUTPUT="+outputFile, "QUALIFICATION_RECORD="+record, "QUALIFICATION_STATUS="+tc.qualification)
			output, err := cmd.CombinedOutput()
			if (err == nil) != (tc.want != "") || !strings.Contains(string(output), tc.diagnostic) {
				t.Fatalf("admission: %v\n%s", err, output)
			}
			admitted, _ := os.ReadFile(outputFile)
			if string(admitted) != tc.want {
				t.Fatalf("admitted %q, want %q", admitted, tc.want)
			}
			checked, _ := os.ReadFile(record)
			if tc.name == "qualified snapshot" && string(checked) != selected {
				t.Fatalf("qualification checked %q instead of %s", checked, selected)
			}
		})
	}
}

func TestReleaseCompatibilityCannotBeSkippedForEitherChannel(t *testing.T) {
	jobs := hostedJobs(t, "release")
	stable, guard, corpus, wire := false, false, false, false
	for _, step := range jobs["classify"].Steps {
		if strings.Contains(step.Run, "/releases/latest") {
			stable = true
			if step.If != "" || step.Continue || !strings.Contains(step.Run, "release-compatibility-baseline.py") || !strings.Contains(step.Run, `--publish "${PUBLISH}"`) || step.Env["SOURCE_SHA"] != "${{ steps.source.outputs.source_sha }}" {
				t.Fatal("published stable baseline is not mandatory for the selected source")
			}
		}
	}
	for _, step := range jobs["compatibility"].Steps {
		if strings.Contains(step.Run, "--compatibility-only") {
			guard = true
			if step.If != "" || step.Continue || step.Env["BASELINE_SHA"] != "${{ needs.classify.outputs.baseline_sha }}" || !strings.Contains(step.Run, "--source-root") || !strings.Contains(step.Run, "--published-versions") || step.Env["PUBLISHED_VERSIONS"] != "${{ needs.classify.outputs.published_versions }}" {
				t.Fatal("compatibility enforcement can skip a release or inspect another source")
			}
		}
		if strings.Contains(step.Run, "./task openapi:diff") {
			wire = true
			if step.Continue || !strings.Contains(step.Run, "--baseline-ref") {
				t.Fatal("OpenAPI report failures or its stable baseline are not enforced")
			}
		}
	}
	for _, step := range jobs["preflight"].Steps {
		if strings.Contains(step.Run, "release:preflight") {
			if step.Env["BASELINE_SHA"] != "${{ needs.classify.outputs.baseline_sha }}" || strings.Count(step.Run, `--baseline-ref "${BASELINE_SHA}"`) < 2 {
				t.Fatal("preflight does not pass pinned baseline_sha to both publish and dry-run paths")
			}
			corpus = corpus || strings.Contains(step.Run, "--require-corpus")
		}
	}
	for _, job := range []string{"preflight", "build-release"} {
		if !slices.Contains(hostedNeeds(t, jobs[job]), "compatibility") {
			t.Fatalf("%s can start before cheap compatibility checks", job)
		}
	}
	for _, step := range jobs["compatibility"].Steps {
		if strings.Contains(step.Uses, "setup-verification") || strings.Contains(step.Uses, "setup-bialy") || strings.Contains(step.Run, "setup-dev") || strings.Contains(step.Run, "build:lycaon") {
			t.Fatal("compatibility gate provisions or builds the app")
		}
	}
	if !stable || !guard || !corpus || !wire {
		t.Fatalf("release assurance missing: stable=%v guard=%v corpus=%v wire=%v", stable, guard, corpus, wire)
	}
	if !slices.Contains(hostedNeeds(t, jobs["upgrade"]), "preflight") {
		t.Fatal("upgrade rehearsal bypasses compatibility preflight")
	}
}
