package portablepython

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestExtendedWorkloadsManifestIntegrity(t *testing.T) {
	t.Parallel()
	root := filepath.Join("testdata", "extended-workloads")
	raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	contractcheck.FailErr(t, "read extended-workloads manifest.json", err)
	var manifest map[string]string
	contractcheck.FailErr(t, "parse extended-workloads manifest.json", json.Unmarshal(raw, &manifest))
	if len(manifest) == 0 {
		t.Fatal("extended-workloads manifest.json is empty")
	}
	seen := make(map[string]bool, len(manifest))
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "manifest.json" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		want, ok := manifest[rel]
		if !ok {
			t.Errorf("unlisted file in extended-workloads: %s", rel)
			return nil
		}
		if got := sha256Hex(data); got != want {
			t.Errorf("digest mismatch for %s: want %s, got %s", rel, want, got)
		}
		seen[rel] = true
		return nil
	})
	contractcheck.FailErr(t, "walk extended-workloads", err)
	for rel := range manifest {
		if !seen[rel] {
			t.Errorf("missing manifest file %s", rel)
		}
	}
}

func copyDirTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	contractcheck.FailErr(t, "copy tree "+src, err)
}

func TestExtendedWorkloadsReferenceReplay(t *testing.T) {
	if os.Getenv("PW_RUN_REFERENCE_REPLAY") != "1" {
		t.Skip("set PW_RUN_REFERENCE_REPLAY=1 to run reference CPython 3.12 replay for extended workloads")
	}
	pyBin := os.Getenv("PW_REFERENCE_PYTHON")
	if pyBin == "" {
		pyBin = "python3"
	}
	pyVersion, pkgVersions := probePythonEnv(t, pyBin)
	baseDir := filepath.Join("testdata", "extended-workloads")

	report := referenceReplayReport{
		OracleVersion:   OracleSchemaVersion,
		GraderVersion:   GraderVersion,
		PythonBinary:    pyBin,
		PythonVersion:   pyVersion,
		PackageVersions: pkgVersions,
	}

	// 1. calculated-edit: edit src-layout module + add test, then fresh call imports & runs streamed provenance.
	calcDir := t.TempDir()
	copyDirTree(t, filepath.Join(baseDir, "calculated-edit"), calcDir)
	runRefPython(t, pyBin, calcDir, "edit_program.py")
	runRefPython(t, pyBin, calcDir, "verify_program.py")
	provBytes, err := os.ReadFile(filepath.Join(calcDir, "provenance_result.json"))
	contractcheck.FailErr(t, "read provenance_result.json", err)
	var provRes struct {
		SHA256         string `json:"sha256"`
		ReadChunkSizes []int  `json:"read_chunk_sizes"`
		MaxChunkSize   int    `json:"max_chunk_size"`
	}
	contractcheck.FailErr(t, "parse provenance_result.json", json.Unmarshal(provBytes, &provRes))
	alphaBytes, err := os.ReadFile(filepath.Join(calcDir, "inputs", "alpha.bin"))
	contractcheck.FailErr(t, "read alpha.bin", err)
	betaBytes, err := os.ReadFile(filepath.Join(calcDir, "inputs", "beta.bin"))
	contractcheck.FailErr(t, "read beta.bin", err)
	contractcheck.FailErr(t, "grade streamed provenance", GradeStreamedProvenance(
		map[string][]byte{"alpha.bin": alphaBytes, "beta.bin": betaBytes},
		StreamedProvenanceObs{
			CandidateSHA256: provRes.SHA256,
			ReadChunkSizes:  provRes.ReadChunkSizes,
			MaxChunkSize:    provRes.MaxChunkSize,
		},
	))
	editProgBytes, err := os.ReadFile(filepath.Join(baseDir, "calculated-edit", "edit_program.py"))
	contractcheck.FailErr(t, "read edit_program.py", err)
	report.Replays = append(report.Replays, referenceReplayEntry{
		Workflow:      "calculated-edit",
		Command:       []string{pyBin, "edit_program.py", "&&", pyBin, "verify_program.py"},
		ProgramPath:   "extended-workloads/calculated-edit/edit_program.py",
		ProgramSHA256: sha256Hex(editProgBytes),
		OutputsSHA256: map[string]string{"provenance_result.json": sha256Hex(provBytes)},
		Verdict:       "pass",
	})

	// 2. project-math-cwd: run eval_program.py and unittest discover with cwd="bench" (failing then repaired).
	mathDir := t.TempDir()
	copyDirTree(t, filepath.Join(baseDir, "project-math-cwd"), mathDir)
	benchDir := filepath.Join(mathDir, "bench")
	runRefPython(t, pyBin, benchDir, "eval_program.py")
	evalBytes, err := os.ReadFile(filepath.Join(benchDir, "eval_result.json"))
	contractcheck.FailErr(t, "read eval_result.json", err)
	var evalRes struct {
		Ratios               map[string]string `json:"ratios"`
		InvalidInputRejected bool              `json:"invalid_input_rejected"`
	}
	contractcheck.FailErr(t, "parse eval_result.json", json.Unmarshal(evalBytes, &evalRes))
	ratios := make(map[[3]int64]string, len(evalRes.Ratios))
	for k, v := range evalRes.Ratios {
		parts := strings.Split(k, ",")
		if len(parts) != 3 {
			t.Fatalf("unexpected ratio key %q", k)
		}
		c, _ := strconv.ParseInt(parts[0], 10, 64)
		n, _ := strconv.ParseInt(parts[1], 10, 64)
		kv, _ := strconv.ParseInt(parts[2], 10, 64)
		ratios[[3]int64{c, n, kv}] = v
	}
	testPath := filepath.Join(benchDir, "test_sampling.py")
	origTest, err := os.ReadFile(testPath)
	contractcheck.FailErr(t, "read test_sampling.py", err)
	brokenTest := strings.Replace(string(origTest), "Fraction(3, 10)", "Fraction(9, 10)", 1)
	contractcheck.FailErr(t, "inject failing assertion", os.WriteFile(testPath, []byte(brokenTest), 0o644))
	failCmd := exec.Command(pyBin, "-B", "-m", "unittest", "discover")
	failCmd.Dir = benchDir
	failOut, failErr := failCmd.CombinedOutput()
	failCode := 0
	if failErr != nil {
		if ee, ok := failErr.(*exec.ExitError); ok {
			failCode = ee.ExitCode()
		} else {
			failCode = 1
		}
	}
	contractcheck.FailErr(t, "repair test_sampling.py", os.WriteFile(testPath, origTest, 0o644))
	runRefPython(t, pyBin, benchDir, "-B", "-m", "unittest", "discover")
	contractcheck.FailErr(t, "grade project math and tests", GradeProjectMathAndTests(ProjectMathObs{
		Ratios:               ratios,
		InvalidInputRejected: evalRes.InvalidInputRejected,
		FailRun:              UnittestRunObs{ExitCode: failCode, Stderr: string(failOut), Passed: false},
		PassRun:              UnittestRunObs{ExitCode: 0, Passed: true},
	}))
	samplingBytes, err := os.ReadFile(filepath.Join(baseDir, "project-math-cwd", "bench", "sampling.py"))
	contractcheck.FailErr(t, "read sampling.py", err)
	report.Replays = append(report.Replays, referenceReplayEntry{
		Workflow:      "project-math-cwd",
		Command:       []string{pyBin, "eval_program.py", "&&", pyBin, "-m", "unittest", "discover"},
		ProgramPath:   "extended-workloads/project-math-cwd/bench/sampling.py",
		ProgramSHA256: sha256Hex(samplingBytes),
		OutputsSHA256: map[string]string{"eval_result.json": sha256Hex(evalBytes)},
		Verdict:       "pass",
	})

	// 3. split-preservation: reject late invalid JSONL + group overlap preserving bytes, then deterministic valid split.
	splitDir := t.TempDir()
	copyDirTree(t, filepath.Join(baseDir, "split-preservation"), splitDir)
	outDir := filepath.Join(splitDir, "out")
	copyDirTree(t, filepath.Join(splitDir, "seed"), outDir)
	readSplitDir := func(dir string) map[string][]byte {
		entries, err := os.ReadDir(dir)
		contractcheck.FailErr(t, "readdir "+dir, err)
		m := make(map[string][]byte, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			contractcheck.FailErr(t, "read "+e.Name(), err)
			m[e.Name()] = b
		}
		return m
	}
	origSplits := readSplitDir(outDir)

	invCmd := exec.Command(pyBin, "splitter.py", "inputs/late_invalid.jsonl", "out")
	invCmd.Dir = splitDir
	invRejected := invCmd.Run() != nil
	afterInvalid := readSplitDir(outDir)

	ovlCmd := exec.Command(pyBin, "splitter.py", "inputs/group_overlap.jsonl", "out")
	ovlCmd.Dir = splitDir
	ovlRejected := ovlCmd.Run() != nil
	afterOverlap := readSplitDir(outDir)

	runRefPython(t, pyBin, splitDir, "splitter.py", "inputs/valid.jsonl", "out1")
	runRefPython(t, pyBin, splitDir, "splitter.py", "inputs/valid.jsonl", "out2")
	validRun1 := readSplitDir(filepath.Join(splitDir, "out1"))
	validRun2 := readSplitDir(filepath.Join(splitDir, "out2"))
	expectedValid := map[string][]byte{
		"train.jsonl": []byte("{\"group\":\"g1\",\"id\":1,\"split\":\"train\"}\n{\"group\":\"g1\",\"id\":2,\"split\":\"train\"}\n"),
		"val.jsonl":   []byte("{\"group\":\"g2\",\"id\":3,\"split\":\"val\"}\n"),
	}
	contractcheck.FailErr(t, "grade split preservation", GradeSplitPreservation(SplitPreservationObs{
		Original:        origSplits,
		AfterInvalid:    afterInvalid,
		InvalidRejected: invRejected,
		AfterOverlap:    afterOverlap,
		OverlapRejected: ovlRejected,
		ExpectedValid:   expectedValid,
		ValidRun1:       validRun1,
		ValidRun2:       validRun2,
	}))
	splitterBytes, err := os.ReadFile(filepath.Join(baseDir, "split-preservation", "splitter.py"))
	contractcheck.FailErr(t, "read splitter.py", err)
	report.Replays = append(report.Replays, referenceReplayEntry{
		Workflow:      "split-preservation",
		Command:       []string{pyBin, "splitter.py", "inputs/valid.jsonl", "out1"},
		ProgramPath:   "extended-workloads/split-preservation/splitter.py",
		ProgramSHA256: sha256Hex(splitterBytes),
		OutputsSHA256: map[string]string{
			"train.jsonl": sha256Hex(validRun1["train.jsonl"]),
			"val.jsonl":   sha256Hex(validRun1["val.jsonl"]),
		},
		Verdict: "pass",
	})

	sort.Slice(report.Replays, func(i, j int) bool { return report.Replays[i].Workflow < report.Replays[j].Workflow })
	encoded, err := json.MarshalIndent(report, "", "  ")
	contractcheck.FailErr(t, "marshal extended replay report", err)
	encoded = append(encoded, '\n')
	if outPath := os.Getenv("PW_EXTENDED_REPLAY_REPORT"); outPath != "" {
		contractcheck.FailErr(t, "write extended replay report", os.WriteFile(outPath, encoded, 0o644))
	}
	t.Logf("extended workloads reference CPython 3.12 replay report:\n%s", string(encoded))
}
