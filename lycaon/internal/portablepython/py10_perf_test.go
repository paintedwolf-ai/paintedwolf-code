package portablepython_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

// PY08 baseline sizes (v0.1.0 + pp-cpython-3.12.0-wasi-82ab6eac12146157) for size-growth tracking.
const (
	py08ImageArchiveBytes        int64 = 20088778
	py08DarwinEngineArchiveBytes int64 = 12377635
	py08DarwinLibraryBytes       int64 = 46590432
	py08LinuxEngineArchiveBytes  int64 = 11770999
	py08LinuxLibraryBytes        int64 = 31841616
)

type py10RunSample struct {
	Kind                  string  `json:"kind"` // "cold" or "warm"
	Index                 int     `json:"index"`
	Backend               string  `json:"backend"`
	ServiceInitMs         float64 `json:"service_init_ms,omitempty"`
	LaunchToFirstResultMs float64 `json:"launch_to_first_result_ms,omitempty"`
	ToolLatencyMs         float64 `json:"tool_latency_ms"`
	RuntimeDurationMs     float64 `json:"runtime_duration_ms"`
	GuestWork             uint64  `json:"guest_work"`
	HostWork              uint64  `json:"host_work"`
	PeakMemoryPages       uint32  `json:"peak_memory_pages"`
	PeakMemoryBytes       uint64  `json:"peak_memory_bytes"`
	CleanCompletion       bool    `json:"clean_completion"`
	ExitCode              int     `json:"exit_code"`
}

type py10WorkloadResult struct {
	Name                            string          `json:"name"`
	ColdRuns                        []py10RunSample `json:"cold_runs"`
	WarmRuns                        []py10RunSample `json:"warm_runs"`
	ColdServiceInitMedianMs         float64         `json:"cold_service_init_median_ms"`
	ColdLaunchToFirstResultMedianMs float64         `json:"cold_launch_to_first_result_median_ms"`
	WarmToolLatencyMedianMs         float64         `json:"warm_tool_latency_median_ms"`
	WarmGuestWorkMedian             uint64          `json:"warm_guest_work_median"`
	WarmHostWorkMedian              uint64          `json:"warm_host_work_median"`
	WarmPeakMemoryPagesMedian       uint32          `json:"warm_peak_memory_pages_median"`
	WarmPeakMemoryBytesMedian       uint64          `json:"warm_peak_memory_bytes_median"`
	ArtifactSHA256                  string          `json:"artifact_sha256,omitempty"`
	ArtifactBytes                   int64           `json:"artifact_bytes,omitempty"`
}

type py10SizeComparison struct {
	PY08ImageArchiveBytes   int64 `json:"py08_image_archive_bytes"`
	PY10ImageArchiveBytes   int64 `json:"py10_image_archive_bytes,omitempty"`
	ImageArchiveDeltaBytes  int64 `json:"image_archive_delta_bytes,omitempty"`
	PY10StagedImageBytes    int64 `json:"py10_staged_image_bytes"`
	PY08EngineArchiveBytes  int64 `json:"py08_engine_archive_bytes"`
	PY10EngineArchiveBytes  int64 `json:"py10_engine_archive_bytes,omitempty"`
	EngineArchiveDeltaBytes int64 `json:"engine_archive_delta_bytes,omitempty"`
	PY08LibraryBytes        int64 `json:"py08_library_bytes"`
	PY10LibraryBytes        int64 `json:"py10_library_bytes"`
	LibraryDeltaBytes       int64 `json:"library_delta_bytes"`
}

type py10PerfReceipt struct {
	SchemaVersion      string                              `json:"schema_version"`
	RecordedAt         string                              `json:"recorded_at"`
	Platform           string                              `json:"platform"`
	GOOS               string                              `json:"goos"`
	GOARCH             string                              `json:"goarch"`
	GoVersion          string                              `json:"go_version"`
	Revision           string                              `json:"revision"`
	SelectedBackend    string                              `json:"selected_backend"`
	NativeEngine       *pythonruntime.NativeEngineIdentity `json:"native_engine,omitempty"`
	ImageDir           string                              `json:"image_dir"`
	ImageID            string                              `json:"image_id"`
	ManifestSHA256     string                              `json:"manifest_sha256"`
	ImageOverrideUnset bool                                `json:"image_override_unset"`
	EngineOverrideUnset bool                               `json:"engine_override_unset"`
	HostLoadBefore     string                              `json:"host_load_before"`
	SizeComparison     py10SizeComparison                  `json:"size_comparison"`
	Workloads          []py10WorkloadResult                `json:"workloads"`
	WorkbookGatePassed bool                                `json:"workbook_gate_passed"`
}

const py10SympyScript = `
import sympy as sp
from fractions import Fraction

x, y = sp.symbols("x y")
sol = sp.solve([sp.Eq(2*x + 3*y, 7), sp.Eq(4*x - y, 5)], (x, y))
assert Fraction(sol[x].p, sol[x].q) == Fraction(11, 7), f"bad x={sol[x]}"
assert Fraction(sol[y].p, sol[y].q) == Fraction(9, 7), f"bad y={sol[y]}"

expr = x**3 - 3*x**2 + 2*x + 1
deriv = sp.diff(expr, x)
assert deriv.subs(x, sp.Rational(11, 7)) == sp.Rational(164, 49)
print("SYMPY_OK", sol[x], sol[y], deriv.subs(x, sp.Rational(11, 7)))
`

const py10PillowScript = `
from PIL import Image, ImageOps

img = Image.open("input_exif.jpg")
transposed = ImageOps.exif_transpose(img)
resized = transposed.resize((120, 80), Image.Resampling.LANCZOS)
resized.save("converted.png", format="PNG")

chk = Image.open("converted.png")
assert chk.size == (120, 80), f"bad size {chk.size}"
assert chk.format == "PNG"
print("PILLOW_OK", chk.size, chk.mode)
`

const py10OfficeScript = `
from pathlib import Path
from PIL import Image
from docx import Document
from pptx import Presentation
from pptx.chart.data import CategoryChartData
from pptx.enum.chart import XL_CHART_TYPE
from pptx.util import Inches
from fpdf import FPDF

# 1. Create a small landmark PNG for embedding in DOCX, PPTX, and PDF
img = Image.new("RGB", (120, 60), color=(30, 90, 160))
for x in range(10, 50):
    for y in range(10, 50):
        img.putpixel((x, y), (220, 140, 40))
img.save("landmark.png", format="PNG")

# 2. DOCX: create template, reopen, edit cell, add paragraph & PNG, save report.docx
seed = Document()
seed.add_heading("PY10 Quarterly Report", level=1)
tbl = seed.add_table(rows=2, cols=2)
tbl.cell(0, 0).text = "Metric"
tbl.cell(0, 1).text = "Value"
tbl.cell(1, 0).text = "Status"
tbl.cell(1, 1).text = "DRAFT"
seed.save("seed.docx")

doc = Document("seed.docx")
doc.tables[0].cell(1, 1).text = "VERIFIED_PY10"
doc.add_paragraph("Updated summary paragraph with Unicode: café — 100%.")
doc.add_picture("landmark.png", width=Inches(1.5))
doc.save("report.docx")

# 3. PPTX: 2-slide deck with text, PNG, and chart
prs = Presentation()
s1 = prs.slides.add_slide(prs.slide_layouts[5])
s1.shapes.title.text = "PY10 Slide 1: Overview"
s1.shapes.add_picture("landmark.png", Inches(1), Inches(2), width=Inches(2))

s2 = prs.slides.add_slide(prs.slide_layouts[5])
s2.shapes.title.text = "PY10 Slide 2: Metrics"
cd = CategoryChartData()
cd.categories = ["Q1", "Q2", "Q3"]
cd.add_series("Throughput", (12.5, 18.0, 24.5))
s2.shapes.add_chart(XL_CHART_TYPE.COLUMN_CLUSTERED, Inches(1), Inches(2), Inches(6), Inches(4), cd)
prs.save("deck.pptx")

# 4. fpdf2: 2-page PDF report with measured page break, table, PNG, Unicode/ASCII text
pdf = FPDF()
pdf.set_margins(20, 20, 20)
pdf.set_auto_page_break(auto=True, margin=20)
pdf.add_page()
pdf.set_font("Helvetica", "B", 16)
pdf.cell(0, 10, "PY10 Portable Python Report - Page 1", new_x="LMARGIN", new_y="NEXT")
pdf.set_font("Helvetica", "", 11)
pdf.multi_cell(0, 7, "Generated inside WASI CPython via fpdf2 with clean 20mm margins and embedded image.")
pdf.ln(4)
pdf.image("landmark.png", x=20, w=45)
pdf.ln(28)
with pdf.table() as table:
    for row in [["Package", "Status"], ["SymPy", "OK"], ["Pillow", "OK"], ["python-docx", "OK"], ["python-pptx", "OK"], ["fpdf2", "OK"]]:
        r = table.row()
        for cell in row:
            r.cell(cell)

pdf.add_page()
pdf.set_font("Helvetica", "B", 16)
pdf.cell(0, 10, "PY10 Portable Python Report - Page 2", new_x="LMARGIN", new_y="NEXT")
pdf.set_font("Helvetica", "", 11)
pdf.multi_cell(0, 7, "Second page verifying explicit page break, margin safety, and non-clipped layout.")
pdf.output("report.pdf")

assert Path("report.docx").stat().st_size > 1000
assert Path("deck.pptx").stat().st_size > 1000
assert Path("report.pdf").stat().st_size > 1000
print("OFFICE_OK")
`

func writeJPEGWithEXIFOrientation6(t *testing.T, dst string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 80, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 80; x++ {
			c := color.RGBA{R: 40, G: 100, B: 180, A: 255}
			if x < 20 && y < 20 {
				c = color.RGBA{R: 240, G: 30, B: 30, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	b := raw.Bytes()
	// Insert minimal APP1 EXIF segment with Orientation=6 (Rotate 90 CW) right after SOI (FF D8).
	exifPayload := []byte{
		'E', 'x', 'i', 'f', 0x00, 0x00,
		'M', 'M', 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08, // Big-endian TIFF header, IFD0 at offset 8
		0x00, 0x01, // 1 IFD entry
		0x01, 0x12, // Tag 0x0112 = Orientation
		0x00, 0x03, // Type SHORT
		0x00, 0x00, 0x00, 0x01, // Count 1
		0x00, 0x06, 0x00, 0x00, // Value 6 (Rotate 90 CW)
		0x00, 0x00, 0x00, 0x00, // Next IFD = 0
	}
	segLen := len(exifPayload) + 2
	var out bytes.Buffer
	out.Write(b[:2]) // SOI
	out.Write([]byte{0xFF, 0xE1, byte(segLen >> 8), byte(segLen & 0xFF)})
	out.Write(exifPayload)
	out.Write(b[2:])
	if err := os.WriteFile(dst, out.Bytes(), 0o644); err != nil {
		t.Fatalf("write input_exif.jpg: %v", err)
	}
}

func medianFloat64(vals []float64) float64 {
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

func medianUint64(vals []uint64) uint64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]uint64(nil), vals...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp[len(cp)/2]
}

func medianUint32(vals []uint32) uint32 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]uint32(nil), vals...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp[len(cp)/2]
}

func copyDirTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}

func TestPY10_PerfAndSizeMatrix(t *testing.T) {
	if os.Getenv("PW_PY10_PERF") != "1" {
		t.Skip("set PW_PY10_PERF=1 to run PY10 5-run fresh-guest performance and size matrix")
	}
	if os.Getenv("PW_PY10_REQUIRE_INSTALLED") == "1" {
		if strings.TrimSpace(os.Getenv(portablepython.EnvImageDir)) != "" {
			t.Fatalf("%s must be UNSET during PY10 installed-app qualification", portablepython.EnvImageDir)
		}
		if strings.TrimSpace(os.Getenv(pythonruntime.EnvEngineDir)) != "" {
			t.Fatalf("%s must be UNSET during PY10 installed-app qualification", pythonruntime.EnvEngineDir)
		}
	}
	if root := strings.TrimSpace(os.Getenv("PW_QUAL_ENGINE_ROOT")); root != "" && strings.TrimSpace(os.Getenv("LYCAON_ENGINE_ROOT")) == "" {
		t.Setenv("LYCAON_ENGINE_ROOT", root)
	}

	ctx := context.Background()
	coldRunsCount := envIntOrDefault("PW_PY10_PERF_COLD_RUNS", 1)
	warmRunsCount := envIntOrDefault("PW_PY10_PERF_WARM_RUNS", 5)

	// Load frozen workbook fixtures from scripts/portable-python-qual/bench/fixtures
	fixturesDir := filepath.Join("..", "..", "..", "scripts", "portable-python-qual", "bench", "fixtures")
	reposYAML, err := os.ReadFile(filepath.Join(fixturesDir, "repos.yaml"))
	if err != nil {
		t.Fatalf("read repos.yaml fixture: %v", err)
	}
	workbookPy, err := os.ReadFile(filepath.Join(fixturesDir, "workbook.py"))
	if err != nil {
		t.Fatalf("read workbook.py fixture: %v", err)
	}

	type workloadSpec struct {
		name      string
		setupWS   func(t *testing.T, ws string)
		code      string
		script    string
		artifact  string
		exportEnv string
	}

	specs := []workloadSpec{
		{
			name:    "sympy_solve",
			setupWS: func(*testing.T, string) {},
			code:    py10SympyScript,
		},
		{
			name: "pillow_convert",
			setupWS: func(t *testing.T, ws string) {
				writeJPEGWithEXIFOrientation6(t, filepath.Join(ws, "input_exif.jpg"))
			},
			code:     py10PillowScript,
			artifact: "converted.png",
		},
		{
			name:      "office_generation",
			setupWS:   func(*testing.T, string) {},
			code:      py10OfficeScript,
			artifact:  "report.pdf",
			exportEnv: "PW_PY10_PERF_PDF_OUT",
		},
		{
			name: "frozen_workbook_gate",
			setupWS: func(t *testing.T, ws string) {
				if err := os.MkdirAll(filepath.Join(ws, "config"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(ws, "config", "repos.yaml"), reposYAML, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(ws, "workbook.py"), workbookPy, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			script:   "workbook.py",
			artifact: "repositories.xlsx",
		},
	}

	var firstSvcInfo pythonruntime.RuntimeInfo
	var firstImageDir, firstImageID, firstManifestSHA string
	var results []py10WorkloadResult
	workbookGatePassed := false

	for _, sp := range specs {
		res := py10WorkloadResult{Name: sp.name}

		runOnce := func(svc *portablepython.Service, kind string, idx int, initMs float64) (py10RunSample, string) {
			ws := t.TempDir()
			sp.setupWS(t, ws)
			roots := []pythondocuments.HostRootConfig{{ID: "ws-py10", Dir: ws}}
			door := native.NewPythonMutationDoor(tools.ToolContext{}, roots)
			req := portablepython.ToolRequest{
				ExecutionID:  fmt.Sprintf("py10-%s-%s-%d", sp.name, kind, idx),
				CallID:       fmt.Sprintf("call-%s-%s-%d", sp.name, kind, idx),
				SessionID:    "sess-py10-perf",
				Actor:        "coordinator",
				View:         "primary",
				Surface:      "implement_investigate",
				Roots:        roots,
				MutationDoor: door,
			}
			if sp.script != "" {
				req.RawArgs = map[string]any{"script": sp.script}
			} else {
				req.RawArgs = map[string]any{"code": sp.code}
			}
			t0 := time.Now()
			resp, err := svc.ExecuteTool(ctx, req)
			toolMs := float64(time.Since(t0).Microseconds()) / 1000.0
			if err != nil {
				t.Fatalf("%s [%s #%d] error: %v", sp.name, kind, idx, err)
			}
			if !resp.CleanCompletion || resp.ExitCode != 0 {
				t.Fatalf("%s [%s #%d] failed: exit=%d clean=%v stop=%+v stderr=%q", sp.name, kind, idx, resp.ExitCode, resp.CleanCompletion, resp.Stop, resp.Stderr)
			}
			if resp.Backend != pythonruntime.BackendNativeAOT {
				t.Fatalf("%s [%s #%d] expected default backend=%q got %q", sp.name, kind, idx, pythonruntime.BackendNativeAOT, resp.Backend)
			}
			artPath := ""
			if sp.artifact != "" {
				artPath = filepath.Join(ws, sp.artifact)
				if _, statErr := os.Stat(artPath); statErr != nil {
					t.Fatalf("%s [%s #%d] missing expected settled artifact %s: %v", sp.name, kind, idx, sp.artifact, statErr)
				}
				if sp.exportEnv != "" && kind == "warm" && idx == warmRunsCount {
					if dst := strings.TrimSpace(os.Getenv(sp.exportEnv)); dst != "" {
						if b, rerr := os.ReadFile(artPath); rerr == nil {
							_ = os.MkdirAll(filepath.Dir(dst), 0o755)
							_ = os.WriteFile(dst, b, 0o644)
						}
					}
				}
			}
			sample := py10RunSample{
				Kind:              kind,
				Index:             idx,
				Backend:           string(resp.Backend),
				ToolLatencyMs:     toolMs,
				RuntimeDurationMs: float64(resp.Usage.Duration.Microseconds()) / 1000.0,
				GuestWork:         resp.Usage.GuestWork,
				HostWork:          resp.Usage.HostWork,
				PeakMemoryPages:   resp.Usage.PeakMemoryPages,
				PeakMemoryBytes:   uint64(resp.Usage.PeakMemoryPages) * wasmPageSizeBytes,
				CleanCompletion:   resp.CleanCompletion,
				ExitCode:          resp.ExitCode,
			}
			if kind == "cold" {
				sample.ServiceInitMs = initMs
				sample.LaunchToFirstResultMs = initMs + toolMs
			}
			return sample, artPath
		}

		// Cold run(s)
		var coldInitVals, coldLaunchVals []float64
		for i := 1; i <= coldRunsCount; i++ {
			tInit := time.Now()
			svc, err := portablepython.NewService(ctx, portablepython.ServiceConfig{ScratchRoot: t.TempDir()})
			initMs := float64(time.Since(tInit).Microseconds()) / 1000.0
			if err != nil {
				t.Fatalf("%s cold NewService failed: %v", sp.name, err)
			}
			if firstSvcInfo.Backend == "" {
				firstSvcInfo = svc.RuntimeInfo()
				if imgDir, derr := portablepython.DiscoverImageDir(); derr == nil {
					firstImageDir = imgDir
					firstManifestSHA, _ = fileDigestAndSize(filepath.Join(imgDir, "image-manifest.json"))
					if m, merr := pythonruntime.LoadImageManifest(filepath.Join(imgDir, "image-manifest.json")); merr == nil {
						firstImageID = m.ImageID
					}
				}
			}
			sample, _ := runOnce(svc, "cold", i, initMs)
			_ = svc.Close(ctx)
			res.ColdRuns = append(res.ColdRuns, sample)
			coldInitVals = append(coldInitVals, sample.ServiceInitMs)
			coldLaunchVals = append(coldLaunchVals, sample.LaunchToFirstResultMs)
		}

		// 5 fresh-guest warm-service runs
		warmSvc, err := portablepython.NewService(ctx, portablepython.ServiceConfig{ScratchRoot: t.TempDir()})
		if err != nil {
			t.Fatalf("%s warm NewService failed: %v", sp.name, err)
		}
		var warmMsVals []float64
		var warmFuelVals, warmHostVals []uint64
		var warmPageVals []uint32
		var lastArtPath string
		for i := 1; i <= warmRunsCount; i++ {
			sample, artPath := runOnce(warmSvc, "warm", i, 0)
			res.WarmRuns = append(res.WarmRuns, sample)
			warmMsVals = append(warmMsVals, sample.ToolLatencyMs)
			warmFuelVals = append(warmFuelVals, sample.GuestWork)
			warmHostVals = append(warmHostVals, sample.HostWork)
			warmPageVals = append(warmPageVals, sample.PeakMemoryPages)
			if artPath != "" {
				lastArtPath = artPath
			}
		}
		_ = warmSvc.Close(ctx)

		res.ColdServiceInitMedianMs = medianFloat64(coldInitVals)
		res.ColdLaunchToFirstResultMedianMs = medianFloat64(coldLaunchVals)
		res.WarmToolLatencyMedianMs = medianFloat64(warmMsVals)
		res.WarmGuestWorkMedian = medianUint64(warmFuelVals)
		res.WarmHostWorkMedian = medianUint64(warmHostVals)
		res.WarmPeakMemoryPagesMedian = medianUint32(warmPageVals)
		res.WarmPeakMemoryBytesMedian = uint64(res.WarmPeakMemoryPagesMedian) * wasmPageSizeBytes
		if lastArtPath != "" {
			res.ArtifactSHA256, res.ArtifactBytes = fileDigestAndSize(lastArtPath)
		}
		if sp.name == "frozen_workbook_gate" {
			workbookGatePassed = res.WarmToolLatencyMedianMs <= 5000.0
			if !workbookGatePassed {
				t.Errorf("frozen workbook timing gate exceeded: warm median %.1f ms > 5000 ms", res.WarmToolLatencyMedianMs)
			}
		}
		t.Logf("PY10 %s: cold_launch=%.1fms warm_median=%.1fms fuel=%d peak_pages=%d",
			sp.name, res.ColdLaunchToFirstResultMedianMs, res.WarmToolLatencyMedianMs, res.WarmGuestWorkMedian, res.WarmPeakMemoryPagesMedian)
		results = append(results, res)
	}

	// Size comparison vs PY08 baseline
	py08EngArc, py08Lib := py08LinuxEngineArchiveBytes, py08LinuxLibraryBytes
	if runtime.GOOS == "darwin" {
		py08EngArc, py08Lib = py08DarwinEngineArchiveBytes, py08DarwinLibraryBytes
	}
	var py10LibBytes int64
	if firstSvcInfo.NativeEngine != nil && firstSvcInfo.NativeEngine.LibraryPath != "" {
		_, py10LibBytes = fileDigestAndSize(firstSvcInfo.NativeEngine.LibraryPath)
	}
	sizeCmp := py10SizeComparison{
		PY08ImageArchiveBytes:  py08ImageArchiveBytes,
		PY10StagedImageBytes:   dirTotalSizeBytes(firstImageDir),
		PY08EngineArchiveBytes: py08EngArc,
		PY08LibraryBytes:       py08Lib,
		PY10LibraryBytes:       py10LibBytes,
		LibraryDeltaBytes:      py10LibBytes - py08Lib,
	}
	if p := strings.TrimSpace(os.Getenv("PW_PY10_IMAGE_ARCHIVE")); p != "" {
		_, sizeCmp.PY10ImageArchiveBytes = fileDigestAndSize(p)
		sizeCmp.ImageArchiveDeltaBytes = sizeCmp.PY10ImageArchiveBytes - py08ImageArchiveBytes
	}
	if p := strings.TrimSpace(os.Getenv("PW_PY10_ENGINE_ARCHIVE")); p != "" {
		_, sizeCmp.PY10EngineArchiveBytes = fileDigestAndSize(p)
		sizeCmp.EngineArchiveDeltaBytes = sizeCmp.PY10EngineArchiveBytes - py08EngArc
	}

	receipt := py10PerfReceipt{
		SchemaVersion:       "paintedwolf.py10-perf-matrix/v1",
		RecordedAt:          time.Now().UTC().Format(time.RFC3339),
		Platform:            runtime.GOOS + "-" + runtime.GOARCH,
		GOOS:                runtime.GOOS,
		GOARCH:              runtime.GOARCH,
		GoVersion:           runtime.Version(),
		Revision:            strings.TrimSpace(os.Getenv("PW_PY10_REVISION")),
		SelectedBackend:     string(firstSvcInfo.Backend),
		NativeEngine:        firstSvcInfo.NativeEngine,
		ImageDir:            firstImageDir,
		ImageID:             firstImageID,
		ManifestSHA256:      firstManifestSHA,
		ImageOverrideUnset:  strings.TrimSpace(os.Getenv(portablepython.EnvImageDir)) == "",
		EngineOverrideUnset: strings.TrimSpace(os.Getenv(pythonruntime.EnvEngineDir)) == "",
		HostLoadBefore:      strings.TrimSpace(os.Getenv("PW_PY10_HOST_LOAD")),
		SizeComparison:      sizeCmp,
		Workloads:           results,
		WorkbookGatePassed:  workbookGatePassed,
	}

	if outPath := strings.TrimSpace(os.Getenv("PW_PY10_PERF_OUT")); outPath != "" {
		raw, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			t.Fatalf("marshal receipt: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			t.Fatalf("mkdir receipt dir: %v", err)
		}
		if err := os.WriteFile(outPath, append(raw, '\n'), 0o644); err != nil {
			t.Fatalf("write receipt %s: %v", outPath, err)
		}
	}
}

// TestPY10_StalePairAndHostExtensionRefusal verifies fail-closed pre-execution
// refusal when pairing:
//  1. New PY10 engine with old PY08 image (PW_PY10_OLD_IMAGE_DIR)
//  2. Old PY08 engine with new PY10 image (PW_PY10_OLD_ENGINE_DIR)
//  3. A host extension binary (.so/.dylib) planted inside the staged PY10 image
func TestPY10_StalePairAndHostExtensionRefusal(t *testing.T) {
	if os.Getenv("PW_PY10_STALE_REFUSAL") != "1" {
		t.Skip("set PW_PY10_STALE_REFUSAL=1 to run PY10 stale-pair and host-extension refusal checks")
	}
	engineRoot := strings.TrimSpace(os.Getenv("PW_QUAL_ENGINE_ROOT"))
	if engineRoot == "" {
		engineRoot = strings.TrimSpace(os.Getenv("LYCAON_ENGINE_ROOT"))
	}
	if engineRoot == "" {
		t.Fatal("PW_QUAL_ENGINE_ROOT or LYCAON_ENGINE_ROOT is required")
	}

	assertFailClosed := func(t *testing.T, label, testRoot string) {
		t.Helper()
		t.Setenv("LYCAON_ENGINE_ROOT", testRoot)
		t.Setenv(portablepython.EnvImageDir, "")
		t.Setenv(pythonruntime.EnvEngineDir, "")
		svc, err := portablepython.NewService(context.Background(), portablepython.ServiceConfig{
			ScratchRoot: t.TempDir(),
		})
		if err == nil {
			_ = svc.Close(context.Background())
			t.Fatalf("%s: expected fail-closed refusal before guest execution, but NewService succeeded (backend=%s)", label, svc.RuntimeInfo().Backend)
		}
		var verifyErr *pythonruntime.NativeEngineVerificationError
		if errors.As(err, &verifyErr) {
			t.Logf("PASS [%s]: fail-closed [%s]: %v", label, verifyErr.Stage, verifyErr)
		} else {
			t.Logf("PASS [%s]: fail-closed error: %v", label, err)
		}
	}

	t.Run("1_new_engine_with_old_py08_image", func(t *testing.T) {
		oldImg := strings.TrimSpace(os.Getenv("PW_PY10_OLD_IMAGE_DIR"))
		if oldImg == "" {
			t.Skip("PW_PY10_OLD_IMAGE_DIR not set")
		}
		dst := filepath.Join(t.TempDir(), "engine-root")
		if err := copyDirTree(filepath.Join(engineRoot, "portable-python", "engine"), filepath.Join(dst, "portable-python", "engine")); err != nil {
			t.Fatalf("copy new engine: %v", err)
		}
		if err := copyDirTree(oldImg, filepath.Join(dst, "portable-python", "image")); err != nil {
			t.Fatalf("copy old image: %v", err)
		}
		assertFailClosed(t, "new_engine_with_old_py08_image", dst)
	})

	t.Run("2_old_py08_engine_with_new_py10_image", func(t *testing.T) {
		oldEng := strings.TrimSpace(os.Getenv("PW_PY10_OLD_ENGINE_DIR"))
		if oldEng == "" {
			t.Skip("PW_PY10_OLD_ENGINE_DIR not set")
		}
		dst := filepath.Join(t.TempDir(), "engine-root")
		if err := copyDirTree(oldEng, filepath.Join(dst, "portable-python", "engine")); err != nil {
			t.Fatalf("copy old engine: %v", err)
		}
		if err := copyDirTree(filepath.Join(engineRoot, "portable-python", "image"), filepath.Join(dst, "portable-python", "image")); err != nil {
			t.Fatalf("copy new image: %v", err)
		}
		assertFailClosed(t, "old_py08_engine_with_new_py10_image", dst)
	})

	t.Run("3_host_extension_binary_planted_in_image", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "engine-root")
		if err := copyDirTree(filepath.Join(engineRoot, "portable-python"), filepath.Join(dst, "portable-python")); err != nil {
			t.Fatalf("copy staged portable-python: %v", err)
		}
		extName := "evil_native_ext.so"
		if runtime.GOOS == "darwin" {
			extName = "evil_native_ext.dylib"
		}
		planted := filepath.Join(dst, "portable-python", "image", "packages", extName)
		if err := os.WriteFile(planted, []byte{0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00}, 0o755); err != nil {
			t.Fatalf("plant host extension binary: %v", err)
		}
		assertFailClosed(t, "host_extension_binary_planted_in_image", dst)
	})
}
