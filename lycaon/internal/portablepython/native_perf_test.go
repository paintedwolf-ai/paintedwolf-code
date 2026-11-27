package portablepython_test

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

const wasmPageSizeBytes = 65536

type workbookPhaseTiming struct {
	YAMLImportMs           float64 `json:"yaml_import_ms"`
	OpenpyxlImportMs       float64 `json:"openpyxl_import_ms"`
	YAMLParseMs            float64 `json:"yaml_parse_ms"`
	WorkbookBuildSaveMs    float64 `json:"workbook_build_save_ms"`
	WorkbookReopenVerifyMs float64 `json:"workbook_reopen_verify_ms"`
}

type workbookCallRecord struct {
	Kind                  string              `json:"kind"` // "cold" or "warm"
	Index                 int                 `json:"index"`
	Backend               string              `json:"backend"`
	ServiceInitMs         float64             `json:"service_init_ms,omitempty"`
	LaunchToFirstResultMs float64             `json:"launch_to_first_result_ms,omitempty"`
	ToolLatencyMs         float64             `json:"tool_latency_ms"`
	RuntimeDurationMs     float64             `json:"runtime_duration_ms"`
	Phases                workbookPhaseTiming `json:"phases"`
	GuestWork             uint64              `json:"guest_work"`
	HostWork              uint64              `json:"host_work"`
	PeakMemoryPages       uint32              `json:"peak_memory_pages"`
	PeakMemoryBytes       uint64              `json:"peak_memory_bytes"`
	StdoutBytes           int64               `json:"stdout_bytes"`
	StderrBytes           int64               `json:"stderr_bytes"`
	StagedBytes           int64               `json:"staged_bytes"`
	WorkbookBytes         int64               `json:"workbook_bytes"`
	WorkbookSHA256        string              `json:"workbook_sha256"`
	CleanCompletion       bool                `json:"clean_completion"`
	ExitCode              int                 `json:"exit_code"`
}

type phaseMedians struct {
	YAMLImportMs           float64 `json:"yaml_import_ms"`
	OpenpyxlImportMs       float64 `json:"openpyxl_import_ms"`
	YAMLParseMs            float64 `json:"yaml_parse_ms"`
	WorkbookBuildSaveMs    float64 `json:"workbook_build_save_ms"`
	WorkbookReopenVerifyMs float64 `json:"workbook_reopen_verify_ms"`
}

type workbookBenchSummary struct {
	ColdServiceInitMedianMs         float64      `json:"cold_service_init_median_ms"`
	ColdLaunchToFirstResultMedianMs float64      `json:"cold_launch_to_first_result_median_ms"`
	ColdToolLatencyMedianMs         float64      `json:"cold_tool_latency_median_ms"`
	WarmToolLatencyMedianMs         float64      `json:"warm_tool_latency_median_ms"`
	ColdPhasesMedian                phaseMedians `json:"cold_phases_median"`
	WarmPhasesMedian                phaseMedians `json:"warm_phases_median"`
	WarmGuestWorkMedian             uint64       `json:"warm_guest_work_median"`
	WarmHostWorkMedian              uint64       `json:"warm_host_work_median"`
	WarmPeakMemoryPagesMedian       uint32       `json:"warm_peak_memory_pages_median"`
	WarmPeakMemoryBytesMedian       uint64       `json:"warm_peak_memory_bytes_median"`
}

type workbookBenchReceipt struct {
	SchemaVersion      string                              `json:"schema_version"`
	RecordedAt         string                              `json:"recorded_at"`
	ConfigLabel        string                              `json:"config_label"`
	Platform           string                              `json:"platform"`
	GOOS               string                              `json:"goos"`
	GOARCH             string                              `json:"goarch"`
	GoVersion          string                              `json:"go_version"`
	Revision           string                              `json:"revision"`
	RequestedBackend   string                              `json:"requested_backend"`
	DefaultBackendUsed bool                                `json:"default_backend_used"`
	SelectedBackend    string                              `json:"selected_backend"`
	NativeEngine       *pythonruntime.NativeEngineIdentity `json:"native_engine,omitempty"`
	ImageVariant       string                              `json:"image_variant"`
	ImageDir           string                              `json:"image_dir"`
	ImageSizeBytes     int64                               `json:"image_size_bytes"`
	ImageOverrideUnset bool                                `json:"image_override_unset"`
	ImageID            string                              `json:"image_id"`
	InterpreterVersion string                              `json:"interpreter_version"`
	WasmSHA256         string                              `json:"wasm_sha256"`
	StdlibSHA256       string                              `json:"stdlib_sha256"`
	ManifestSHA256     string                              `json:"manifest_sha256"`
	Toolchain          string                              `json:"toolchain"`
	SigningFlags       string                              `json:"signing_flags"`
	HostLoadBefore     string                              `json:"host_load_before"`
	CPUInfo            string                              `json:"cpu_info"`
	BinaryPath         string                              `json:"binary_path,omitempty"`
	BinarySizeBytes    int64                               `json:"binary_size_bytes,omitempty"`
	BinarySHA256       string                              `json:"binary_sha256,omitempty"`
	EngineArtifactPath string                              `json:"engine_artifact_path,omitempty"`
	EngineSizeBytes    int64                               `json:"engine_size_bytes,omitempty"`
	EngineSHA256       string                              `json:"engine_sha256,omitempty"`
	ColdRuns           []workbookCallRecord                `json:"cold_runs"`
	WarmRuns           []workbookCallRecord                `json:"warm_runs"`
	Summary            workbookBenchSummary                `json:"summary"`
}

func envIntOrDefault(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return def
	}
	return v
}

func fileDigestAndSize(path string) (string, int64) {
	if strings.TrimSpace(path) == "" {
		return "", 0
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), int64(len(b))
}

func dirTotalSizeBytes(root string) int64 {
	if strings.TrimSpace(root) == "" {
		return 0
	}
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func parseRequestedBackend(raw string) (pythonruntime.BackendKind, error) {
	v := strings.TrimSpace(raw)
	switch v {
	case "", "default":
		// Empty BackendKind exercises the production default backend route.
		return "", nil
	case "wazero-control":
		if runtime.GOOS == "darwin" {
			return pythonruntime.BackendWazeroInterpreter, nil
		}
		return pythonruntime.BackendWazeroCompiler, nil
	case string(pythonruntime.BackendNativeAOT):
		return pythonruntime.BackendNativeAOT, nil
	case string(pythonruntime.BackendWazeroCompiler):
		return pythonruntime.BackendWazeroCompiler, nil
	case string(pythonruntime.BackendWazeroInterpreter):
		return pythonruntime.BackendWazeroInterpreter, nil
	default:
		return "", fmt.Errorf("unsupported PW_PERF_BACKEND=%q", raw)
	}
}

func parseWorkbookPhases(stdout string) (workbookPhaseTiming, error) {
	marks := make(map[string]float64)
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "PHASE" {
			v, err := strconv.ParseFloat(fields[2], 64)
			if err != nil {
				return workbookPhaseTiming{}, fmt.Errorf("parse phase %q: %w", line, err)
			}
			marks[fields[1]] = v
		}
	}
	required := []string{"start", "yaml_imported", "openpyxl_imported", "yaml_loaded", "saved", "end"}
	for _, k := range required {
		if _, ok := marks[k]; !ok {
			return workbookPhaseTiming{}, fmt.Errorf("missing PHASE marker %q in stdout", k)
		}
	}
	ms := func(a, b string) float64 {
		return math.Round((marks[b]-marks[a])*1000.0*1000.0) / 1000.0
	}
	return workbookPhaseTiming{
		YAMLImportMs:           ms("start", "yaml_imported"),
		OpenpyxlImportMs:       ms("yaml_imported", "openpyxl_imported"),
		YAMLParseMs:            ms("openpyxl_imported", "yaml_loaded"),
		WorkbookBuildSaveMs:    ms("yaml_loaded", "saved"),
		WorkbookReopenVerifyMs: ms("saved", "end"),
	}, nil
}

func medianFloat(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]float64(nil), vals...)
	sort.Float64s(cp)
	mid := len(cp) / 2
	if len(cp)%2 == 1 {
		return math.Round(cp[mid]*1000.0) / 1000.0
	}
	return math.Round(((cp[mid-1]+cp[mid])/2.0)*1000.0) / 1000.0
}

func medianUint64(vals []uint64) uint64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]uint64(nil), vals...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	mid := len(cp) / 2
	if len(cp)%2 == 1 {
		return cp[mid]
	}
	return (cp[mid-1] + cp[mid]) / 2
}

func computePhaseMedians(runs []workbookCallRecord) phaseMedians {
	var yi, oi, yp, wbs, wrv []float64
	for _, r := range runs {
		yi = append(yi, r.Phases.YAMLImportMs)
		oi = append(oi, r.Phases.OpenpyxlImportMs)
		yp = append(yp, r.Phases.YAMLParseMs)
		wbs = append(wbs, r.Phases.WorkbookBuildSaveMs)
		wrv = append(wrv, r.Phases.WorkbookReopenVerifyMs)
	}
	return phaseMedians{
		YAMLImportMs:           medianFloat(yi),
		OpenpyxlImportMs:       medianFloat(oi),
		YAMLParseMs:            medianFloat(yp),
		WorkbookBuildSaveMs:    medianFloat(wbs),
		WorkbookReopenVerifyMs: medianFloat(wrv),
	}
}

func verifySettledWorkbook(t *testing.T, path string) (int64, string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settled workbook %s: %v", path, err)
	}
	zr, err := zip.NewReader(strings.NewReader(string(b)), int64(len(b)))
	if err != nil {
		t.Fatalf("open settled workbook zip %s: %v", path, err)
	}
	seen := make(map[string]bool, len(zr.File))
	for _, f := range zr.File {
		seen[f.Name] = true
	}
	for _, req := range []string{"xl/workbook.xml", "xl/worksheets/sheet1.xml", "xl/worksheets/sheet2.xml"} {
		if !seen[req] {
			t.Fatalf("settled workbook %s missing expected ZIP member %s", path, req)
		}
	}
	sum := sha256.Sum256(b)
	return int64(len(b)), hex.EncodeToString(sum[:])
}

func runSingleWorkbookCall(
	t *testing.T,
	svc *portablepython.Service,
	expectedBackend pythonruntime.BackendKind,
	script string,
	yamlBytes []byte,
	kind string,
	idx int,
	serviceInitMs float64,
) workbookCallRecord {
	t.Helper()
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "config"), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "config", "repos.yaml"), yamlBytes, 0o644); err != nil {
		t.Fatalf("write repos.yaml: %v", err)
	}
	roots := []pythondocuments.HostRootConfig{{ID: "ws", Dir: ws}}
	callID := fmt.Sprintf("py06-%s-%d", kind, idx)

	start := time.Now()
	resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
		RawArgs:      map[string]any{"code": script},
		Actor:        "coordinator",
		SessionID:    "py06-native-perf",
		CallID:       callID,
		Roots:        roots,
		MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
	})
	elapsedMs := math.Round(float64(time.Since(start).Microseconds())/1000.0*1000.0) / 1000.0
	if err != nil {
		t.Fatalf("%s run %d ExecuteTool failed: %v", kind, idx, err)
	}
	if !resp.CleanCompletion || resp.ExitCode != 0 {
		t.Fatalf("%s run %d did not complete cleanly: exit=%d stop=%+v stderr=%s", kind, idx, resp.ExitCode, resp.Stop, resp.Stderr)
	}
	if resp.Backend != "" && resp.Backend != expectedBackend {
		t.Fatalf("%s run %d backend mismatch: got %q want %q", kind, idx, resp.Backend, expectedBackend)
	}
	phases, err := parseWorkbookPhases(resp.Stdout)
	if err != nil {
		t.Fatalf("%s run %d parse phases: %v\nstdout:\n%s", kind, idx, err, resp.Stdout)
	}
	wbBytes, wbSHA := verifySettledWorkbook(t, filepath.Join(ws, "repositories.xlsx"))

	rec := workbookCallRecord{
		Kind:              kind,
		Index:             idx,
		Backend:           string(expectedBackend),
		ToolLatencyMs:     elapsedMs,
		RuntimeDurationMs: math.Round(float64(resp.Usage.Duration.Microseconds())/1000.0*1000.0) / 1000.0,
		Phases:            phases,
		GuestWork:         resp.Usage.GuestWork,
		HostWork:          resp.Usage.HostWork,
		PeakMemoryPages:   resp.Usage.PeakMemoryPages,
		PeakMemoryBytes:   uint64(resp.Usage.PeakMemoryPages) * wasmPageSizeBytes,
		StdoutBytes:       resp.Usage.StdoutBytes,
		StderrBytes:       resp.Usage.StderrBytes,
		StagedBytes:       resp.Usage.StagedBytes,
		WorkbookBytes:     wbBytes,
		WorkbookSHA256:    wbSHA,
		CleanCompletion:   resp.CleanCompletion,
		ExitCode:          resp.ExitCode,
	}
	if kind == "cold" {
		rec.ServiceInitMs = serviceInitMs
		rec.LaunchToFirstResultMs = math.Round((serviceInitMs+elapsedMs)*1000.0) / 1000.0
	}
	t.Logf("[%s #%d backend=%s] init=%.1fms tool=%.1fms launch_to_first=%.1fms | yaml_imp=%.1fms openpyxl_imp=%.1fms parse=%.1fms build_save=%.1fms reopen=%.1fms | guest_work=%d host_work=%d peak_pages=%d",
		kind, idx, rec.Backend, rec.ServiceInitMs, rec.ToolLatencyMs, rec.LaunchToFirstResultMs,
		phases.YAMLImportMs, phases.OpenpyxlImportMs, phases.YAMLParseMs,
		phases.WorkbookBuildSaveMs, phases.WorkbookReopenVerifyMs,
		rec.GuestWork, rec.HostWork, rec.PeakMemoryPages)
	return rec
}

func resolveFixturePath(envKey, relFromRepo string) (string, error) {
	if p := strings.TrimSpace(os.Getenv(envKey)); p != "" {
		return p, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for i := 0; i < 6; i++ {
		cand := filepath.Join(dir, relFromRepo)
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("could not locate %s (set %s)", relFromRepo, envKey)
}

// TestPY06NativePerfWorkbookMatrix runs the PY06 idle-host benchmark matrix:
// 5 cold calls (each from a freshly constructed Service) followed by 5 warm
// calls (5 fresh guests on a warm Service), writing structured JSON receipts.
// Supports all 4 configurations:
//   - PW_PERF_BACKEND=wazero-control (or wazero-interpreter / wazero-compiler)
//   - PW_PERF_BACKEND="" / "default" (proves default route selects native-aot)
//   - PW_PERF_BACKEND=native-aot
// Guarded by PW_PERF_RUN=1 so standard go test ./... is unaffected.
func TestPY06NativePerfWorkbookMatrix(t *testing.T) {
	if strings.TrimSpace(os.Getenv("PW_PERF_RUN")) != "1" {
		t.Skip("set PW_PERF_RUN=1 to execute the PY06 workbook performance matrix")
	}

	scriptPath, err := resolveFixturePath("PW_PERF_SCRIPT_PATH", filepath.Join("scripts", "portable-python-qual", "bench", "fixtures", "workbook.py"))
	if err != nil {
		t.Fatal(err)
	}
	yamlPath, err := resolveFixturePath("PW_PERF_YAML_PATH", filepath.Join("scripts", "portable-python-qual", "bench", "fixtures", "repos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	scriptBytes, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read workbook script: %v", err)
	}
	yamlBytes, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("read repos.yaml: %v", err)
	}

	rawBackend := strings.TrimSpace(os.Getenv("PW_PERF_BACKEND"))
	reqBackend, err := parseRequestedBackend(rawBackend)
	if err != nil {
		t.Fatal(err)
	}
	defaultBackendUsed := reqBackend == ""

	coldCount := envIntOrDefault("PW_PERF_COLD_RUNS", 5)
	warmCount := envIntOrDefault("PW_PERF_WARM_RUNS", 5)
	explicitImageDir := strings.TrimSpace(os.Getenv("PW_PERF_IMAGE_DIR"))
	explicitEngineDir := strings.TrimSpace(os.Getenv("PW_PERF_ENGINE_DIR"))
	imageOverrideUnset := strings.TrimSpace(os.Getenv(portablepython.EnvImageDir)) == "" && explicitImageDir == ""

	var coldRuns []workbookCallRecord
	var warmService *portablepython.Service
	var resolvedImageDir string
	var imageID, interpVersion, wasmSHA, stdlibSHA, manifestSHA string
	var rtInfo pythonruntime.RuntimeInfo

	for i := 1; i <= coldCount; i++ {
		initStart := time.Now()
		svc, err := portablepython.NewService(context.Background(), portablepython.ServiceConfig{
			ImageDir:    explicitImageDir,
			Backend:     reqBackend,
			EngineDir:   explicitEngineDir,
			ScratchRoot: t.TempDir(),
		})
		initMs := math.Round(float64(time.Since(initStart).Microseconds())/1000.0*1000.0) / 1000.0
		if err != nil {
			t.Fatalf("cold run %d NewService(backend=%q) failed: %v", i, reqBackend, err)
		}
		info := svc.RuntimeInfo()
		if defaultBackendUsed && info.Backend != pythonruntime.BackendNativeAOT {
			_ = svc.Close(context.Background())
			t.Fatalf("default backend route expected %q on %s/%s, got %q", pythonruntime.BackendNativeAOT, runtime.GOOS, runtime.GOARCH, info.Backend)
		}
		if i == 1 {
			rtInfo = info
			resolvedImageDir = svc.ImageDir()
			if m := svc.RuntimeManifest(); m != nil {
				imageID = m.ImageID
				interpVersion = m.Interpreter.Version
				wasmSHA = m.Interpreter.WasmSHA256
				stdlibSHA = m.Interpreter.StdlibSHA256
			}
			manifestSHA, _ = fileDigestAndSize(filepath.Join(resolvedImageDir, "image-manifest.json"))
		}

		rec := runSingleWorkbookCall(t, svc, info.Backend, string(scriptBytes), yamlBytes, "cold", i, initMs)
		coldRuns = append(coldRuns, rec)

		if i == coldCount {
			warmService = svc
		} else {
			_ = svc.Close(context.Background())
		}
	}
	defer func() {
		if warmService != nil {
			_ = warmService.Close(context.Background())
		}
	}()

	var warmRuns []workbookCallRecord
	for i := 1; i <= warmCount; i++ {
		rec := runSingleWorkbookCall(t, warmService, rtInfo.Backend, string(scriptBytes), yamlBytes, "warm", i, 0)
		warmRuns = append(warmRuns, rec)
	}

	var coldInits, coldLaunches, coldLatencies, warmLatencies []float64
	var warmGuestWork, warmHostWork, warmPeakPages, warmPeakBytes []uint64
	for _, r := range coldRuns {
		coldInits = append(coldInits, r.ServiceInitMs)
		coldLaunches = append(coldLaunches, r.LaunchToFirstResultMs)
		coldLatencies = append(coldLatencies, r.ToolLatencyMs)
	}
	for _, r := range warmRuns {
		warmLatencies = append(warmLatencies, r.ToolLatencyMs)
		warmGuestWork = append(warmGuestWork, r.GuestWork)
		warmHostWork = append(warmHostWork, r.HostWork)
		warmPeakPages = append(warmPeakPages, uint64(r.PeakMemoryPages))
		warmPeakBytes = append(warmPeakBytes, r.PeakMemoryBytes)
	}

	summary := workbookBenchSummary{
		ColdServiceInitMedianMs:         medianFloat(coldInits),
		ColdLaunchToFirstResultMedianMs: medianFloat(coldLaunches),
		ColdToolLatencyMedianMs:         medianFloat(coldLatencies),
		WarmToolLatencyMedianMs:         medianFloat(warmLatencies),
		ColdPhasesMedian:                computePhaseMedians(coldRuns),
		WarmPhasesMedian:                computePhaseMedians(warmRuns),
		WarmGuestWorkMedian:             medianUint64(warmGuestWork),
		WarmHostWorkMedian:              medianUint64(warmHostWork),
		WarmPeakMemoryPagesMedian:       uint32(medianUint64(warmPeakPages)),
		WarmPeakMemoryBytesMedian:       medianUint64(warmPeakBytes),
	}

	binPath := strings.TrimSpace(os.Getenv("PW_PERF_BINARY_PATH"))
	if binPath == "" {
		if exe, err := os.Executable(); err == nil {
			binPath = exe
		}
	}
	binSHA, binSize := fileDigestAndSize(binPath)

	enginePath := strings.TrimSpace(os.Getenv("PW_PERF_ENGINE_ARTIFACT_PATH"))
	if enginePath == "" && rtInfo.NativeEngine != nil && strings.TrimSpace(rtInfo.NativeEngine.LibraryPath) != "" {
		enginePath = rtInfo.NativeEngine.LibraryPath
	}
	engineSHA, engineSize := fileDigestAndSize(enginePath)
	if engineSHA == "" && rtInfo.NativeEngine != nil {
		engineSHA = rtInfo.NativeEngine.LibrarySHA256
	}

	receipt := workbookBenchReceipt{
		SchemaVersion:      "portable-python.py06-perf/v1",
		RecordedAt:         time.Now().UTC().Format(time.RFC3339),
		ConfigLabel:        strings.TrimSpace(os.Getenv("PW_PERF_CONFIG_LABEL")),
		Platform:           fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		GOOS:               runtime.GOOS,
		GOARCH:             runtime.GOARCH,
		GoVersion:          runtime.Version(),
		Revision:           strings.TrimSpace(os.Getenv("PW_PERF_REVISION")),
		RequestedBackend:   rawBackend,
		DefaultBackendUsed: defaultBackendUsed,
		SelectedBackend:    string(rtInfo.Backend),
		NativeEngine:       rtInfo.NativeEngine,
		ImageVariant:       strings.TrimSpace(os.Getenv("PW_PERF_IMAGE_VARIANT")),
		ImageDir:           resolvedImageDir,
		ImageSizeBytes:     dirTotalSizeBytes(resolvedImageDir),
		ImageOverrideUnset: imageOverrideUnset,
		ImageID:            imageID,
		InterpreterVersion: interpVersion,
		WasmSHA256:         wasmSHA,
		StdlibSHA256:       stdlibSHA,
		ManifestSHA256:     manifestSHA,
		Toolchain:          strings.TrimSpace(os.Getenv("PW_PERF_TOOLCHAIN")),
		SigningFlags:       strings.TrimSpace(os.Getenv("PW_PERF_SIGNING_FLAGS")),
		HostLoadBefore:     strings.TrimSpace(os.Getenv("PW_PERF_HOST_LOAD")),
		CPUInfo:            strings.TrimSpace(os.Getenv("PW_PERF_CPU_INFO")),
		BinaryPath:         binPath,
		BinarySizeBytes:    binSize,
		BinarySHA256:       binSHA,
		EngineArtifactPath: enginePath,
		EngineSizeBytes:    engineSize,
		EngineSHA256:       engineSHA,
		ColdRuns:           coldRuns,
		WarmRuns:           warmRuns,
		Summary:            summary,
	}

	outJSON, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	outJSON = append(outJSON, '\n')

	if outPath := strings.TrimSpace(os.Getenv("PW_PERF_OUT_JSON")); outPath != "" {
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			t.Fatalf("mkdir out dir: %v", err)
		}
		if err := os.WriteFile(outPath, outJSON, 0o644); err != nil {
			t.Fatalf("write %s: %v", outPath, err)
		}
		t.Logf("wrote JSON receipt to %s", outPath)
	}

	fmt.Printf("PY06_PERF_SUMMARY config=%s backend=%s default=%v variant=%s cold_init_med=%.1fms cold_launch_med=%.1fms cold_tool_med=%.1fms warm_tool_med=%.1fms guest_work=%d host_work=%d peak_pages=%d\n",
		receipt.ConfigLabel, receipt.SelectedBackend, receipt.DefaultBackendUsed, receipt.ImageVariant,
		summary.ColdServiceInitMedianMs, summary.ColdLaunchToFirstResultMedianMs,
		summary.ColdToolLatencyMedianMs, summary.WarmToolLatencyMedianMs,
		summary.WarmGuestWorkMedian, summary.WarmHostWorkMedian, summary.WarmPeakMemoryPagesMedian)
}
