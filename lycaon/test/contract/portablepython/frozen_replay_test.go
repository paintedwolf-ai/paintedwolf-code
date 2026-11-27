package portablepython

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type referenceReplayEntry struct {
	Workflow      string            `json:"workflow"`
	Command       []string          `json:"command"`
	ProgramPath   string            `json:"program_path"`
	ProgramSHA256 string            `json:"program_sha256"`
	OutputsSHA256 map[string]string `json:"outputs_sha256"`
	Verdict       string            `json:"verdict"`
}

type referenceReplayReport struct {
	OracleVersion   string                 `json:"oracle_version"`
	GraderVersion   string                 `json:"grader_version"`
	PythonBinary    string                 `json:"python_binary"`
	PythonVersion   string                 `json:"python_version"`
	PackageVersions map[string]string      `json:"package_versions"`
	Replays         []referenceReplayEntry `json:"replays"`
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func extractWorkflowDir(t *testing.T, files map[string][]byte, prefix string) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		if !strings.HasPrefix(name, prefix+"/") {
			continue
		}
		rel := strings.TrimPrefix(name, prefix+"/")
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		contractcheck.FailErr(t, "mkdir "+dst, os.MkdirAll(filepath.Dir(dst), 0o755))
		contractcheck.FailErr(t, "write "+dst, os.WriteFile(dst, data, 0o644))
	}
	return dir
}

func runRefPython(t *testing.T, pyBin, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(pyBin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run %s %v in %s: %v\noutput:\n%s", pyBin, args, dir, err, string(out))
	}
	return string(out)
}

func probePythonEnv(t *testing.T, pyBin string) (string, map[string]string) {
	t.Helper()
	verOut, err := exec.Command(pyBin, "--version").CombinedOutput()
	contractcheck.FailErr(t, "python3 --version", err)
	pkgOut, err := exec.Command(pyBin, "-c",
		`import json, yaml, openpyxl; print(json.dumps({"PyYAML": yaml.__version__, "openpyxl": openpyxl.__version__}))`,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("probe python packages with %s: %v\noutput:\n%s", pyBin, err, string(pkgOut))
	}
	var pkgs map[string]string
	contractcheck.FailErr(t, "parse package versions", json.Unmarshal(bytes.TrimSpace(pkgOut), &pkgs))
	pyVer := strings.TrimSpace(string(verOut))
	if os.Getenv("PW_ALLOW_NON_PY312") != "1" && !strings.HasPrefix(pyVer, "Python 3.12.") {
		t.Fatalf("reference CPython must match shipped 3.12 minor version (got %q; set PW_REFERENCE_PYTHON to Python 3.12)", pyVer)
	}
	return pyVer, pkgs
}

func TestFrozenAgentProgramsReferenceReplay(t *testing.T) {
	if os.Getenv("PW_RUN_REFERENCE_REPLAY") != "1" {
		t.Skip("set PW_RUN_REFERENCE_REPLAY=1 to run reference CPython replay against retained agent programs")
	}

	pyBin := os.Getenv("PW_REFERENCE_PYTHON")
	if pyBin == "" {
		pyBin = "python3"
	}
	pyVersion, pkgVersions := probePythonEnv(t, pyBin)
	files := loadFrozenFixtureZip(t)

	report := referenceReplayReport{
		OracleVersion:   OracleSchemaVersion,
		GraderVersion:   GraderVersion,
		PythonBinary:    pyBin,
		PythonVersion:   pyVersion,
		PackageVersions: pkgVersions,
	}

	// 1. dependency-review
	depDir := extractWorkflowDir(t, files, "dependency-review")
	runRefPython(t, pyBin, depDir, "program.py")
	depCSV, err := os.ReadFile(filepath.Join(depDir, "dependency-changes.csv"))
	contractcheck.FailErr(t, "read dependency-changes.csv", err)
	depExp, err := ComputeDepDiff(
		files["dependency-review/before/uv.lock"],
		files["dependency-review/after/uv.lock"],
		files["dependency-review/before/train-host/requirements-cuda.txt"],
		files["dependency-review/after/train-host/requirements-cuda.txt"],
	)
	contractcheck.FailErr(t, "compute dep diff", err)
	contractcheck.FailErr(t, "grade dependency-changes.csv", GradeDependencyDiff(depExp, depCSV, map[string][]string{"before": nil, "after": nil}))
	report.Replays = append(report.Replays, referenceReplayEntry{
		Workflow:      "dependency-review",
		Command:       []string{pyBin, "program.py"},
		ProgramPath:   "dependency-review/program.py",
		ProgramSHA256: sha256Hex(files["dependency-review/program.py"]),
		OutputsSHA256: map[string]string{"dependency-changes.csv": sha256Hex(depCSV)},
		Verdict:       "pass",
	})

	// 2. licenses
	licDir := extractWorkflowDir(t, files, "licenses")
	runRefPython(t, pyBin, licDir, "program.py")
	licCSV, err := os.ReadFile(filepath.Join(licDir, "holdout_repos.csv"))
	contractcheck.FailErr(t, "read holdout_repos.csv", err)
	contractcheck.FailErr(t, "grade holdout_repos.csv", GradeLicenseCSV(files["licenses/config/repos.yaml"], licCSV))
	report.Replays = append(report.Replays, referenceReplayEntry{
		Workflow:      "licenses",
		Command:       []string{pyBin, "program.py"},
		ProgramPath:   "licenses/program.py",
		ProgramSHA256: sha256Hex(files["licenses/program.py"]),
		OutputsSHA256: map[string]string{"holdout_repos.csv": sha256Hex(licCSV)},
		Verdict:       "pass",
	})

	// 3. workbook (real openpyxl-generated .xlsx!)
	wbDir := extractWorkflowDir(t, files, "workbook")
	runRefPython(t, pyBin, wbDir, "program.py")
	wbBytes, err := os.ReadFile(filepath.Join(wbDir, "repositories.xlsx"))
	contractcheck.FailErr(t, "read repositories.xlsx", err)
	contractcheck.FailErr(t, "grade openpyxl repositories.xlsx (frozen replay)", GradeRepoWorkbookWithMode(files["workbook/config/repos.yaml"], wbBytes, WorkbookFrozenReplay))
	contractcheck.FailErr(t, "grade openpyxl repositories.xlsx (live mode)", GradeRepoWorkbookWithMode(files["workbook/config/repos.yaml"], wbBytes, WorkbookLiveDeliverable))
	report.Replays = append(report.Replays, referenceReplayEntry{
		Workflow:      "workbook",
		Command:       []string{pyBin, "program.py"},
		ProgramPath:   "workbook/program.py",
		ProgramSHA256: sha256Hex(files["workbook/program.py"]),
		OutputsSHA256: map[string]string{"repositories.xlsx": sha256Hex(wbBytes)},
		Verdict:       "pass",
	})

	// 4. release-index
	relDir := extractWorkflowDir(t, files, "release-index")
	runRefPython(t, pyBin, relDir, "program.py")
	relJSON, err := os.ReadFile(filepath.Join(relDir, "release-index.json"))
	contractcheck.FailErr(t, "read release-index.json", err)
	relAnchors := make(map[string][]byte)
	for name, data := range files {
		if strings.HasPrefix(name, "release-index/releases/") && strings.HasSuffix(name, ".json") {
			relAnchors[path.Base(name)] = data
		}
	}
	contractcheck.FailErr(t, "grade release-index.json", GradeReleaseIndex(relAnchors, relJSON))
	report.Replays = append(report.Replays, referenceReplayEntry{
		Workflow:      "release-index",
		Command:       []string{pyBin, "program.py"},
		ProgramPath:   "release-index/program.py",
		ProgramSHA256: sha256Hex(files["release-index/program.py"]),
		OutputsSHA256: map[string]string{"release-index.json": sha256Hex(relJSON)},
		Verdict:       "pass",
	})

	// 5. markdown-links
	mdDir := extractWorkflowDir(t, files, "markdown-links")
	runRefPython(t, pyBin, mdDir, "program.py")
	linksCSV, err := os.ReadFile(filepath.Join(mdDir, "links.csv"))
	contractcheck.FailErr(t, "read links.csv", err)
	mdInputs := make(map[string]string)
	for name, data := range files {
		if strings.HasPrefix(name, "markdown-links/") && strings.HasSuffix(name, ".md") {
			mdInputs[strings.TrimPrefix(name, "markdown-links/")] = string(data)
		}
	}
	contractcheck.FailErr(t, "grade links.csv", GradeMarkdownLinks(mdInputs, linksCSV))
	report.Replays = append(report.Replays, referenceReplayEntry{
		Workflow:      "markdown-links",
		Command:       []string{pyBin, "program.py"},
		ProgramPath:   "markdown-links/program.py",
		ProgramSHA256: sha256Hex(files["markdown-links/program.py"]),
		OutputsSHA256: map[string]string{"links.csv": sha256Hex(linksCSV)},
		Verdict:       "pass",
	})

	// 6. archive (run twice with distinct output paths!)
	archDir := extractWorkflowDir(t, files, "archive")
	runRefPython(t, pyBin, archDir, "build_handoff.py", "handoff-a.zip")
	runRefPython(t, pyBin, archDir, "build_handoff.py", "handoff-b.zip")
	zipA, err := os.ReadFile(filepath.Join(archDir, "handoff-a.zip"))
	contractcheck.FailErr(t, "read handoff-a.zip", err)
	zipB, err := os.ReadFile(filepath.Join(archDir, "handoff-b.zip"))
	contractcheck.FailErr(t, "read handoff-b.zip", err)
	archAnchors := make(map[string][]byte)
	for name, data := range files {
		if strings.HasPrefix(name, "archive/releases/") && strings.HasSuffix(name, ".json") {
			archAnchors[path.Base(name)] = data
		}
	}
	contractcheck.FailErr(t, "grade reproducible archive", GradeReleaseArchive(archAnchors, zipA, zipB))
	report.Replays = append(report.Replays, referenceReplayEntry{
		Workflow:      "archive",
		Command:       []string{pyBin, "build_handoff.py", "handoff-a.zip", "&&", pyBin, "build_handoff.py", "handoff-b.zip"},
		ProgramPath:   "archive/build_handoff.py",
		ProgramSHA256: sha256Hex(files["archive/build_handoff.py"]),
		OutputsSHA256: map[string]string{
			"handoff-a.zip": sha256Hex(zipA),
			"handoff-b.zip": sha256Hex(zipB),
		},
		Verdict: "pass",
	})

	sort.Slice(report.Replays, func(i, j int) bool { return report.Replays[i].Workflow < report.Replays[j].Workflow })
	encoded, err := json.MarshalIndent(report, "", "  ")
	contractcheck.FailErr(t, "marshal replay report", err)
	encoded = append(encoded, '\n')
	if outPath := os.Getenv("PW_REFERENCE_REPLAY_REPORT"); outPath != "" {
		contractcheck.FailErr(t, "write replay report", os.WriteFile(outPath, encoded, 0o644))
	}
	t.Logf("reference CPython replay report:\n%s", string(encoded))
}
