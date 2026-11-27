package portablepython_test

import (
	"context"
	"encoding/json"
	"fmt"
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

type pairedSample struct {
	Index                 int     `json:"index"`
	Mode                  string  `json:"mode"` // "cold", "warm-priming", or "warm"
	Backend               string  `json:"backend"`
	ServiceInstanceID     string  `json:"service_instance_id"`
	InvocationID          string  `json:"invocation_id"`
	ServiceConstructionMS float64 `json:"service_construction_ms"`
	ToolLatencyMS         float64 `json:"tool_latency_ms"`
	RuntimeDurationMS     float64 `json:"runtime_duration_ms"`
	TotalTimeMS           float64 `json:"total_time_ms"`
	Fuel                  uint64  `json:"fuel"`
	HostWork              uint64  `json:"host_work"`
	PeakMemoryPages       uint32  `json:"peak_memory_pages"`
	PeakMemoryBytes       uint64  `json:"peak_memory_bytes"`
	PeakMemoryMiB         float64 `json:"peak_memory_mib"`
	CleanCompletion       bool    `json:"clean_completion"`
	ExitCode              int     `json:"exit_code"`
}

type pairedModeResult struct {
	Mode                        string         `json:"mode"`
	SampleCount                 int            `json:"sample_count"`
	InitServiceConstructionMS   float64        `json:"init_service_construction_ms,omitempty"`
	PrimingSample               *pairedSample  `json:"priming_sample_excluded,omitempty"`
	Samples                     []pairedSample `json:"samples"`
	MedianServiceConstructionMS float64        `json:"median_service_construction_ms"`
	MedianToolLatencyMS         float64        `json:"median_tool_latency_ms"`
	P95ToolLatencyMS            float64        `json:"p95_tool_latency_ms"`
	MedianTotalTimeMS           float64        `json:"median_total_time_ms"`
	P95TotalTimeMS              float64        `json:"p95_total_time_ms"`
	MedianFuel                  uint64         `json:"median_fuel"`
	MedianHostWork              uint64         `json:"median_host_work"`
	MedianPeakMemoryPages       uint32         `json:"median_peak_memory_pages"`
	MedianPeakMemoryBytes       uint64         `json:"median_peak_memory_bytes"`
	MedianPeakMemoryMiB         float64        `json:"median_peak_memory_mib"`
}

type pairedWorkloadResult struct {
	Workload string            `json:"workload"`
	Cold     *pairedModeResult `json:"cold,omitempty"`
	Warm     *pairedModeResult `json:"warm,omitempty"`
}

type pairedArtifactSizes struct {
	ImageArchiveBytes  int64 `json:"image_archive_bytes,omitempty"`
	EngineArchiveBytes int64 `json:"engine_archive_bytes,omitempty"`
	StagedImageBytes   int64 `json:"staged_image_bytes"`
	EngineLibraryBytes int64 `json:"engine_library_bytes"`
}

type pairedBaselineReceipt struct {
	SchemaVersion       string                              `json:"schema_version"`
	RecordedAt          string                              `json:"recorded_at"`
	Label               string                              `json:"label"`
	Platform            string                              `json:"platform"`
	GOOS                string                              `json:"goos"`
	GOARCH              string                              `json:"goarch"`
	GoVersion           string                              `json:"go_version"`
	LogicalCPUs         int                                 `json:"logical_cpus"`
	HarnessRevision     string                              `json:"harness_revision"`
	ProductRevision     string                              `json:"product_revision"`
	SelectedBackend     string                              `json:"selected_backend"`
	NativeEngine        *pythonruntime.NativeEngineIdentity `json:"native_engine,omitempty"`
	ImageDir            string                              `json:"image_dir"`
	ImageID             string                              `json:"image_id"`
	ManifestSHA256      string                              `json:"manifest_sha256"`
	ImageOverrideUnset  bool                                `json:"image_override_unset"`
	EngineOverrideUnset bool                                `json:"engine_override_unset"`
	ColdCachePolicy     string                              `json:"cold_cache_policy"`
	Sizes               pairedArtifactSizes                 `json:"sizes"`
	Workloads           []pairedWorkloadResult              `json:"workloads"`
	WorkbookGatePassed  bool                                `json:"workbook_gate_passed"`
}

func pairedMedianFloat64(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]float64(nil), vals...)
	sort.Float64s(cp)
	mid := len(cp) / 2
	if len(cp)%2 == 1 {
		return cp[mid]
	}
	return (cp[mid-1] + cp[mid]) / 2
}

func pairedP95Float64(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]float64(nil), vals...)
	sort.Float64s(cp)
	idx := int(math.Ceil(0.95*float64(len(cp)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}

func pairedMedianUint64(vals []uint64) uint64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]uint64(nil), vals...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp[len(cp)/2]
}

func pairedMedianUint32(vals []uint32) uint32 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]uint32(nil), vals...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp[len(cp)/2]
}

func pairedDirSizeBytes(root string) int64 {
	var total int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func pairedFileSizeBytes(path string) int64 {
	if strings.TrimSpace(path) == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func pairedSummarizeMode(mode string, initMS float64, samples []pairedSample) *pairedModeResult {
	var constr, tool, total []float64
	var fuels, hosts []uint64
	var pages []uint32
	for _, s := range samples {
		constr = append(constr, s.ServiceConstructionMS)
		tool = append(tool, s.ToolLatencyMS)
		total = append(total, s.TotalTimeMS)
		fuels = append(fuels, s.Fuel)
		hosts = append(hosts, s.HostWork)
		pages = append(pages, s.PeakMemoryPages)
	}
	medPages := pairedMedianUint32(pages)
	medBytes := uint64(medPages) * wasmPageSizeBytes
	return &pairedModeResult{
		Mode:                        mode,
		SampleCount:                 len(samples),
		InitServiceConstructionMS:   initMS,
		Samples:                     samples,
		MedianServiceConstructionMS: pairedMedianFloat64(constr),
		MedianToolLatencyMS:         pairedMedianFloat64(tool),
		P95ToolLatencyMS:            pairedP95Float64(tool),
		MedianTotalTimeMS:           pairedMedianFloat64(total),
		P95TotalTimeMS:              pairedP95Float64(total),
		MedianFuel:                  pairedMedianUint64(fuels),
		MedianHostWork:              pairedMedianUint64(hosts),
		MedianPeakMemoryPages:       medPages,
		MedianPeakMemoryBytes:       medBytes,
		MedianPeakMemoryMiB:         float64(medBytes) / (1024.0 * 1024.0),
	}
}

func pairedEnvInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func TestPY10_PairedBaseline(t *testing.T) {
	if os.Getenv("PW_PY10_PAIRED_BASELINE") != "1" {
		t.Skip("set PW_PY10_PAIRED_BASELINE=1 to run paired baseline measurements")
	}

	imageOverrideUnset := strings.TrimSpace(os.Getenv(portablepython.EnvImageDir)) == ""
	engineOverrideUnset := strings.TrimSpace(os.Getenv(pythonruntime.EnvEngineDir)) == ""
	if os.Getenv("PW_PY10_REQUIRE_INSTALLED") == "1" {
		if !imageOverrideUnset {
			t.Fatalf("%s must be UNSET during installed paired baseline qualification", portablepython.EnvImageDir)
		}
		if !engineOverrideUnset {
			t.Fatalf("%s must be UNSET during installed paired baseline qualification", pythonruntime.EnvEngineDir)
		}
	}

	if root := strings.TrimSpace(os.Getenv("PW_QUAL_ENGINE_ROOT")); root != "" && strings.TrimSpace(os.Getenv("LYCAON_ENGINE_ROOT")) == "" {
		t.Setenv("LYCAON_ENGINE_ROOT", root)
	}

	samplesCount := pairedEnvInt("PW_PAIRED_SAMPLES", 5)
	workloadsCSV := strings.TrimSpace(os.Getenv("PW_PAIRED_WORKLOADS"))
	if workloadsCSV == "" {
		workloadsCSV = "pass_noop,frozen_workbook"
	}
	modesCSV := strings.TrimSpace(os.Getenv("PW_PAIRED_MODES"))
	if modesCSV == "" {
		modesCSV = "cold,warm"
	}
	wantMode := map[string]bool{}
	for _, m := range strings.Split(modesCSV, ",") {
		m = strings.TrimSpace(m)
		if m != "" {
			wantMode[m] = true
		}
	}

	fixturesDir := filepath.Join("..", "..", "..", "scripts", "portable-python-qual", "bench", "fixtures")
	reposYAML, err := os.ReadFile(filepath.Join(fixturesDir, "repos.yaml"))
	if err != nil {
		t.Fatalf("read repos.yaml fixture: %v", err)
	}
	workbookPy, err := os.ReadFile(filepath.Join(fixturesDir, "workbook.py"))
	if err != nil {
		t.Fatalf("read workbook.py fixture: %v", err)
	}

	type workloadDef struct {
		name     string
		setupWS  func(t *testing.T, ws string)
		code     string
		script   string
		artifact string
	}
	catalog := map[string]workloadDef{
		"pass_noop": {
			name:    "pass_noop",
			setupWS: func(*testing.T, string) {},
			code:    "pass\n",
		},
		"frozen_workbook": {
			name: "frozen_workbook",
			setupWS: func(t *testing.T, ws string) {
				if err := os.MkdirAll(filepath.Join(ws, "config"), 0o755); err != nil {
					t.Fatalf("mkdir config: %v", err)
				}
				if err := os.WriteFile(filepath.Join(ws, "config", "repos.yaml"), reposYAML, 0o644); err != nil {
					t.Fatalf("write repos.yaml: %v", err)
				}
				if err := os.WriteFile(filepath.Join(ws, "workbook.py"), workbookPy, 0o644); err != nil {
					t.Fatalf("write workbook.py: %v", err)
				}
			},
			script:   "workbook.py",
			artifact: "repositories.xlsx",
		},
	}

	ctx := context.Background()
	receipt := pairedBaselineReceipt{
		SchemaVersion:       "paintedwolf.portable-python.paired-baseline/v1",
		RecordedAt:          time.Now().UTC().Format(time.RFC3339),
		Label:               strings.TrimSpace(os.Getenv("PW_PAIRED_LABEL")),
		Platform:            fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH),
		GOOS:                runtime.GOOS,
		GOARCH:              runtime.GOARCH,
		GoVersion:           runtime.Version(),
		LogicalCPUs:         runtime.NumCPU(),
		HarnessRevision:     strings.TrimSpace(os.Getenv("PW_PAIRED_HARNESS_REV")),
		ProductRevision:     strings.TrimSpace(os.Getenv("PW_PAIRED_PRODUCT_REV")),
		ImageOverrideUnset:  imageOverrideUnset,
		EngineOverrideUnset: engineOverrideUnset,
		ColdCachePolicy:     "fresh-service-normal-os-page-cache-no-purge",
		Sizes: pairedArtifactSizes{
			ImageArchiveBytes:  pairedFileSizeBytes(os.Getenv("PW_PAIRED_IMAGE_ARCHIVE")),
			EngineArchiveBytes: pairedFileSizeBytes(os.Getenv("PW_PAIRED_ENGINE_ARCHIVE")),
		},
		WorkbookGatePassed: true,
	}
	if receipt.Label == "" {
		receipt.Label = "baseline"
	}

	svcSeq := 0
	svcInstanceIDs := make(map[*portablepython.Service]string)

	newServiceTimed := func(t *testing.T, scope string) (*portablepython.Service, float64) {
		t.Helper()
		t0 := time.Now()
		svc, err := portablepython.NewService(ctx, portablepython.ServiceConfig{
			ScratchRoot: t.TempDir(),
		})
		constrMS := float64(time.Since(t0).Microseconds()) / 1000.0
		if err != nil {
			t.Fatalf("NewService (%s): %v", scope, err)
		}
		svcSeq++
		svcInstanceIDs[svc] = fmt.Sprintf("svc-%d-%p", svcSeq, svc)
		info := svc.RuntimeInfo()
		if info.Backend != pythonruntime.BackendNativeAOT {
			_ = svc.Close(ctx)
			t.Fatalf("expected default backend %q, got %q", pythonruntime.BackendNativeAOT, info.Backend)
		}
		if receipt.SelectedBackend == "" {
			receipt.SelectedBackend = string(info.Backend)
			receipt.NativeEngine = info.NativeEngine
			if imgDir, derr := portablepython.DiscoverImageDir(); derr == nil {
				receipt.ImageDir = imgDir
				receipt.Sizes.StagedImageBytes = pairedDirSizeBytes(imgDir)
				receipt.ManifestSHA256, _ = fileDigestAndSize(filepath.Join(imgDir, "image-manifest.json"))
				if mf, merr := pythonruntime.LoadImageManifest(filepath.Join(imgDir, "image-manifest.json")); merr == nil {
					receipt.ImageID = mf.ImageID
				}
			}
			if info.NativeEngine != nil {
				receipt.Sizes.EngineLibraryBytes = pairedFileSizeBytes(info.NativeEngine.LibraryPath)
			}
		}
		return svc, constrMS
	}

	runGuest := func(t *testing.T, svc *portablepython.Service, def workloadDef, mode string, idx int, constrMS float64) pairedSample {
		t.Helper()
		ws := t.TempDir()
		def.setupWS(t, ws)
		roots := []pythondocuments.HostRootConfig{{ID: "ws-paired", Dir: ws}}
		door := native.NewPythonMutationDoor(tools.ToolContext{}, roots)
		req := portablepython.ToolRequest{
			ExecutionID:  fmt.Sprintf("paired-%s-%s-%s-%d", receipt.Label, def.name, mode, idx),
			CallID:       fmt.Sprintf("call-%s-%s-%s-%d", receipt.Label, def.name, mode, idx),
			SessionID:    "sess-paired-baseline",
			Actor:        "coordinator",
			View:         "primary",
			Surface:      "implement_investigate",
			Roots:        roots,
			MutationDoor: door,
		}
		if def.script != "" {
			req.RawArgs = map[string]any{"script": def.script}
		} else {
			req.RawArgs = map[string]any{"code": def.code}
		}
		t0 := time.Now()
		resp, err := svc.ExecuteTool(ctx, req)
		toolMS := float64(time.Since(t0).Microseconds()) / 1000.0
		if err != nil {
			t.Fatalf("%s [%s #%d] ExecuteTool error: %v", def.name, mode, idx, err)
		}
		if resp.ExitCode != 0 || !resp.CleanCompletion || resp.Stop != nil {
			t.Fatalf("%s [%s #%d] failed: exit=%d clean=%v stop=%+v stderr=%q",
				def.name, mode, idx, resp.ExitCode, resp.CleanCompletion, resp.Stop, resp.Stderr)
		}
		if resp.Backend != pythonruntime.BackendNativeAOT {
			t.Fatalf("%s [%s #%d] expected default backend %q, got %q", def.name, mode, idx, pythonruntime.BackendNativeAOT, resp.Backend)
		}
		if def.artifact != "" {
			artPath := filepath.Join(ws, def.artifact)
			if _, statErr := os.Stat(artPath); statErr != nil {
				t.Fatalf("%s [%s #%d] missing expected settled artifact %s: %v", def.name, mode, idx, def.artifact, statErr)
			}
		}
		peakBytes := uint64(resp.Usage.PeakMemoryPages) * wasmPageSizeBytes
		svcID := svcInstanceIDs[svc]
		if svcID == "" {
			svcID = fmt.Sprintf("svc-untracked-%p", svc)
		}
		invID := resp.ExecutionID
		if invID == "" {
			invID = req.ExecutionID
		}
		return pairedSample{
			Index:                 idx,
			Mode:                  mode,
			Backend:               string(resp.Backend),
			ServiceInstanceID:     svcID,
			InvocationID:          invID,
			ServiceConstructionMS: constrMS,
			ToolLatencyMS:         toolMS,
			RuntimeDurationMS:     float64(resp.Usage.Duration.Microseconds()) / 1000.0,
			TotalTimeMS:           constrMS + toolMS,
			Fuel:                  resp.Usage.GuestWork,
			HostWork:              resp.Usage.HostWork,
			PeakMemoryPages:       resp.Usage.PeakMemoryPages,
			PeakMemoryBytes:       peakBytes,
			PeakMemoryMiB:         float64(peakBytes) / (1024.0 * 1024.0),
			CleanCompletion:       resp.CleanCompletion,
			ExitCode:              resp.ExitCode,
		}
	}

	for _, rawName := range strings.Split(workloadsCSV, ",") {
		name := strings.TrimSpace(rawName)
		if name == "" {
			continue
		}
		def, ok := catalog[name]
		if !ok {
			t.Fatalf("unknown paired workload %q", name)
		}
		wr := pairedWorkloadResult{Workload: def.name}

		if wantMode["cold"] {
			coldSamples := make([]pairedSample, 0, samplesCount)
			for i := 1; i <= samplesCount; i++ {
				svc, constrMS := newServiceTimed(t, fmt.Sprintf("%s-cold-%d", def.name, i))
				s := runGuest(t, svc, def, "cold", i, constrMS)
				_ = svc.Close(ctx)
				coldSamples = append(coldSamples, s)
			}
			wr.Cold = pairedSummarizeMode("cold", 0, coldSamples)
			t.Logf("PAIRED %s [cold n=%d]: median_construct=%.1fms median_tool=%.1fms median_total=%.1fms fuel=%d peak_pages=%d",
				def.name, samplesCount,
				wr.Cold.MedianServiceConstructionMS, wr.Cold.MedianToolLatencyMS, wr.Cold.MedianTotalTimeMS,
				wr.Cold.MedianFuel, wr.Cold.MedianPeakMemoryPages)
		}

		if wantMode["warm"] {
			svc, initMS := newServiceTimed(t, fmt.Sprintf("%s-warm", def.name))
			// Execute 1 excluded priming call on the initialized service before measuring 5 subsequent fresh guests.
			priming := runGuest(t, svc, def, "warm-priming", 0, 0)
			warmSamples := make([]pairedSample, 0, samplesCount)
			for i := 1; i <= samplesCount; i++ {
				s := runGuest(t, svc, def, "warm", i, 0)
				warmSamples = append(warmSamples, s)
			}
			_ = svc.Close(ctx)
			wr.Warm = pairedSummarizeMode("warm", initMS, warmSamples)
			wr.Warm.PrimingSample = &priming
			t.Logf("PAIRED %s [warm n=%d, priming=%.1fms excluded]: init=%.1fms median_tool=%.1fms p95_tool=%.1fms fuel=%d peak_pages=%d",
				def.name, samplesCount, priming.ToolLatencyMS, initMS,
				wr.Warm.MedianToolLatencyMS, wr.Warm.P95ToolLatencyMS,
				wr.Warm.MedianFuel, wr.Warm.MedianPeakMemoryPages)
			if def.name == "frozen_workbook" && wr.Warm.MedianToolLatencyMS > 5000.0 {
				receipt.WorkbookGatePassed = false
				t.Errorf("frozen_workbook warm median %.1fms exceeds 5000.0ms gate", wr.Warm.MedianToolLatencyMS)
			}
		}

		receipt.Workloads = append(receipt.Workloads, wr)
	}

	if outPath := strings.TrimSpace(os.Getenv("PW_PAIRED_OUT_JSON")); outPath != "" {
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(outPath), err)
		}
		raw, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			t.Fatalf("marshal receipt: %v", err)
		}
		if err := os.WriteFile(outPath, append(raw, '\n'), 0o644); err != nil {
			t.Fatalf("write %s: %v", outPath, err)
		}
	}
}
