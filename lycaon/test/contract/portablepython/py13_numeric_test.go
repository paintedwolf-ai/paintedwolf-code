package portablepython

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func py13FixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join("testdata", "py13", name)
	b, err := os.ReadFile(p)
	contractcheck.FailErr(t, "read py13 fixture "+name, err)
	return b
}

func copyPY13Fixture(t *testing.T, wsDir, name string) {
	t.Helper()
	dst := filepath.Join(wsDir, name)
	contractcheck.FailErr(t, "mkdir fixture parent", os.MkdirAll(filepath.Dir(dst), 0o755))
	contractcheck.FailErr(t, "write fixture "+name, os.WriteFile(dst, py13FixtureBytes(t, name), 0o644))
}

func newPY13Service(t *testing.T, receiptDir string) *portablepython.Service {
	t.Helper()
	requireStrict := os.Getenv("PW_REQUIRE_PY13") == "1" || os.Getenv("PW_REQUIRE_NATIVE_QUAL") == "1"
	if requireStrict {
		if strings.TrimSpace(os.Getenv(portablepython.EnvImageDir)) != "" {
			t.Fatalf("%s must be UNSET during PY13 qualification", portablepython.EnvImageDir)
		}
		if strings.TrimSpace(os.Getenv(pythonruntime.EnvEngineDir)) != "" {
			t.Fatalf("%s must be UNSET during PY13 qualification", pythonruntime.EnvEngineDir)
		}
	}
	if root := strings.TrimSpace(os.Getenv("PW_QUAL_ENGINE_ROOT")); root != "" && strings.TrimSpace(os.Getenv("LYCAON_ENGINE_ROOT")) == "" {
		t.Setenv("LYCAON_ENGINE_ROOT", root)
	}
	cfg := portablepython.ServiceConfig{
		ScratchRoot: t.TempDir(),
	}
	if receiptDir != "" {
		sink, err := portablepython.NewFileReceiptStore(receiptDir)
		contractcheck.FailErr(t, "create FileReceiptStore", err)
		cfg.ReceiptSink = sink
	}
	svc, err := portablepython.NewService(context.Background(), cfg)
	if err != nil {
		if requireStrict {
			t.Fatalf("failed to initialize PY13 Portable Python service: %v", err)
		}
		t.Skipf("no staged Portable Python service available (set LYCAON_ENGINE_ROOT): %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	info := svc.RuntimeInfo()
	if info.Backend != pythonruntime.BackendNativeAOT {
		t.Fatalf("PY13 default RuntimeInfo.Backend=%q want %q on %s/%s", info.Backend, pythonruntime.BackendNativeAOT, runtime.GOOS, runtime.GOARCH)
	}
	if info.NativeEngine == nil || info.NativeEngine.EngineTag != "pw-cpython-wasi-engine-v0.5.0" {
		t.Fatalf("PY13 RuntimeInfo.NativeEngine=%+v want engine_tag=pw-cpython-wasi-engine-v0.5.0", info.NativeEngine)
	}
	return svc
}

func runPY13Code(t *testing.T, svc *portablepython.Service, wsDir, execID, code string) *portablepython.ToolResponse {
	t.Helper()
	roots := []pythondocuments.HostRootConfig{{ID: "ws", Dir: wsDir}}
	resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
		ExecutionID:  execID,
		CallID:       execID + "-call",
		SessionID:    "py13-numeric-qual",
		Actor:        "coordinator",
		View:         "primary",
		Surface:      "implement_investigate",
		RawArgs:      map[string]any{"code": code},
		Roots:        roots,
		MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
	})
	contractcheck.FailErr(t, "execute "+execID, err)
	if !resp.CleanCompletion || resp.ExitCode != 0 {
		t.Fatalf("%s failed: exit=%d clean=%v stop=%+v diag=%v usage=%+v stdout=%q stderr=%q",
			execID, resp.ExitCode, resp.CleanCompletion, resp.Stop, resp.RuntimeDiagnostics, resp.Usage, resp.Stdout, resp.Stderr)
	}
	if resp.Backend != pythonruntime.BackendNativeAOT {
		t.Fatalf("%s ran on backend=%q want %q", execID, resp.Backend, pythonruntime.BackendNativeAOT)
	}
	return resp
}

func TestPY13NumericQualification(t *testing.T) {
	receiptDir := t.TempDir()
	svc := newPY13Service(t, receiptDir)
	info := svc.RuntimeInfo()

	type qualSummary struct {
		GOOS                 string  `json:"goos"`
		GOARCH               string  `json:"goarch"`
		EngineTag            string  `json:"engine_tag"`
		CPythonWasmSHA256    string  `json:"cpython_wasm_sha256"`
		NumPyImportMS        float64 `json:"numpy_import_ms"`
		NumPyPeakMemPages    uint32  `json:"numpy_peak_memory_pages"`
		PandasImportMS       float64 `json:"pandas_import_ms"`
		PandasPeakMemPages   uint32  `json:"pandas_peak_memory_pages"`
		KernelCancelMS       float64 `json:"kernel_cancel_ms"`
		KernelFuelExhausted  bool    `json:"kernel_fuel_exhausted"`
		FreshRecoveryOK      bool    `json:"fresh_recovery_ok"`
		StalePairRefusedBoth bool    `json:"stale_pair_refused_both"`
	}
	summary := qualSummary{
		GOOS:              runtime.GOOS,
		GOARCH:            runtime.GOARCH,
		EngineTag:         info.NativeEngine.EngineTag,
		CPythonWasmSHA256: info.NativeEngine.CPythonWasmSHA256,
	}

	t.Run("01_numpy_broadcast_slice_matmul", func(t *testing.T) {
		ws := t.TempDir()
		code := `
import json, time
t0 = time.monotonic()
import numpy as np
imp_ms = (time.monotonic() - t0) * 1000.0

# Fixed inputs: broadcasting, slicing, matrix multiplication, lapack_lite, fft, random
a = np.array([[1.0, 2.0, 3.0, 4.0], [5.0, 6.0, 7.0, 8.0], [9.0, 10.0, 11.0, 12.0]])
b = np.array([10.0, 20.0, 30.0, 40.0])
broad = a + b
expected_broad = [[11.0, 22.0, 33.0, 44.0], [15.0, 26.0, 37.0, 48.0], [19.0, 30.0, 41.0, 52.0]]
assert broad.tolist() == expected_broad, broad

sliced = broad[1:, 1:3]
assert sliced.tolist() == [[26.0, 37.0], [30.0, 41.0]], sliced

m1 = np.array([[1.0, 2.0, 3.0], [4.0, 5.0, 6.0]])
m2 = np.array([[7.0, 8.0], [9.0, 10.0], [11.0, 12.0]])
mm = m1 @ m2
assert mm.tolist() == [[58.0, 64.0], [139.0, 154.0]], mm

# lapack_lite linear solve + det + svd
sq = np.array([[3.0, 1.0], [1.0, 2.0]])
sol = np.linalg.solve(sq, np.array([9.0, 8.0]))
assert np.allclose(sol, [2.0, 3.0]), sol
assert np.isclose(np.linalg.det(sq), 5.0)
u, sv, vt = np.linalg.svd(sq)
assert np.allclose(u @ np.diag(sv) @ vt, sq)

# FFT + modern & legacy RNG
assert np.allclose(np.fft.rfft([1.0, 0.0, -1.0, 0.0]), [0.0, 2.0, 0.0])
np.random.seed(42)
assert np.random.poisson(5.0) >= 0
assert np.random.default_rng(42).poisson(5.0) >= 0

print(json.dumps({"numpy_version": np.__version__, "import_ms": round(imp_ms, 2)}))
`
		resp := runPY13Code(t, svc, ws, "py13-numpy", code)
		var out struct {
			NumPyVersion string  `json:"numpy_version"`
			ImportMS     float64 `json:"import_ms"`
		}
		contractcheck.FailErr(t, "parse numpy json", json.Unmarshal([]byte(strings.TrimSpace(resp.Stdout)), &out))
		if out.NumPyVersion != "1.26.4" {
			t.Fatalf("numpy_version=%q want 1.26.4", out.NumPyVersion)
		}
		summary.NumPyImportMS = out.ImportMS
		summary.NumPyPeakMemPages = resp.Usage.PeakMemoryPages
		t.Logf("numpy 1.26.4 import=%.2fms wall=%dms peak_pages=%d work=%d",
			out.ImportMS, resp.Usage.Duration.Milliseconds(), resp.Usage.PeakMemoryPages, resp.Usage.GuestWork)
	})

	t.Run("02_pandas_csv_join_groupby_sqlite_workspace_receipts", func(t *testing.T) {
		ws := t.TempDir()
		copyPY13Fixture(t, ws, "orders.csv")
		copyPY13Fixture(t, ws, "lookup.csv")
		copyPY13Fixture(t, ws, "expected_summary.json")

		code := `
import csv, json, sqlite3, time
t0 = time.monotonic()
import pandas as pd
imp_ms = (time.monotonic() - t0) * 1000.0

orders = pd.read_csv(
    "/workspace/orders.csv",
    dtype={"order_id": str, "sku_id": str, "qty": "Int64"},
)
lookup = pd.read_csv(
    "/workspace/lookup.csv",
    dtype={"sku_id": str, "category": str, "unit_price": float},
)

# Verify leading-zero identifiers and missing quantity null treatment
assert orders["order_id"].tolist() == ["0001", "0002", "0003", "0004", "0005", "0006", "0007"]
assert orders["sku_id"].tolist() == ["0010", "0010", "0020", "0099", "0020", "0030", "0030"]
assert pd.isna(orders.loc[1, "qty"]) and pd.isna(orders.loc[6, "qty"])

merged = orders.merge(lookup, on="sku_id", how="left")
assert pd.isna(merged.loc[merged["order_id"] == "0004", "category"].iloc[0])
merged["line_total"] = merged["qty"].astype("float64") * merged["unit_price"]
merged["is_missing_qty"] = merged["qty"].isna().astype("int64")

grouped = (
    merged.groupby("category", dropna=False, as_index=False)
    .agg(
        order_count=("order_id", "count"),
        missing_qty_count=("is_missing_qty", "sum"),
        total_qty=("qty", "sum"),
        total_rev=("line_total", "sum"),
    )
    .sort_values("category", na_position="last")
    .reset_index(drop=True)
)

pd_rows = [
    {
        "category": None if pd.isna(r.category) else str(r.category),
        "order_count": int(r.order_count),
        "missing_qty_count": int(r.missing_qty_count),
        "total_qty": int(r.total_qty),
        "total_rev": float(r.total_rev),
    }
    for r in grouped.itertuples(index=False)
]

# Independent csv + sqlite3 oracle
con = sqlite3.connect(":memory:")
con.execute("CREATE TABLE orders (order_id TEXT, sku_id TEXT, qty INTEGER)")
with open("/workspace/orders.csv", newline="", encoding="utf-8") as fh:
    for r in csv.DictReader(fh):
        con.execute("INSERT INTO orders VALUES (?, ?, ?)", (r["order_id"], r["sku_id"], int(r["qty"]) if r["qty"] else None))
con.execute("CREATE TABLE lookup (sku_id TEXT PRIMARY KEY, category TEXT, unit_price REAL)")
with open("/workspace/lookup.csv", newline="", encoding="utf-8") as fh:
    for r in csv.DictReader(fh):
        con.execute("INSERT INTO lookup VALUES (?, ?, ?)", (r["sku_id"], r["category"], float(r["unit_price"])))

sql_rows = [
    {
        "category": row[0],
        "order_count": int(row[1]),
        "missing_qty_count": int(row[2]),
        "total_qty": int(row[3]),
        "total_rev": float(row[4]),
    }
    for row in con.execute("""
        SELECT l.category,
               COUNT(o.order_id),
               SUM(CASE WHEN o.qty IS NULL THEN 1 ELSE 0 END),
               COALESCE(SUM(o.qty), 0),
               COALESCE(SUM(o.qty * l.unit_price), 0.0)
        FROM orders o
        LEFT JOIN lookup l ON o.sku_id = l.sku_id
        GROUP BY l.category
        ORDER BY l.category IS NULL, l.category
    """).fetchall()
]

with open("/workspace/expected_summary.json", encoding="utf-8") as fh:
    expected = json.load(fh)

assert pd_rows == expected, (pd_rows, expected)
assert pd_rows == sql_rows, (pd_rows, sql_rows)

# Write and reopen through normal workspace view
import os
os.makedirs("/workspace/out", exist_ok=True)
grouped.to_csv("/workspace/out/summary.csv", index=False)
merged[["order_id", "sku_id", "qty", "category"]].to_csv("/workspace/out/joined.csv", index=False)

reopened_joined = pd.read_csv("/workspace/out/joined.csv", dtype={"order_id": str, "sku_id": str, "qty": "Int64"})
assert reopened_joined["order_id"].tolist() == ["0001", "0002", "0003", "0004", "0005", "0006", "0007"]
assert reopened_joined["sku_id"].tolist() == ["0010", "0010", "0020", "0099", "0020", "0030", "0030"]

reopened_summary = pd.read_csv("/workspace/out/summary.csv")
assert len(reopened_summary) == 4

print(json.dumps({"pandas_version": pd.__version__, "import_ms": round(imp_ms, 2), "rows": len(pd_rows)}))
`
		resp := runPY13Code(t, svc, ws, "py13-pandas", code)
		var out struct {
			PandasVersion string  `json:"pandas_version"`
			ImportMS      float64 `json:"import_ms"`
			Rows          int     `json:"rows"`
		}
		contractcheck.FailErr(t, "parse pandas json", json.Unmarshal([]byte(strings.TrimSpace(resp.Stdout)), &out))
		if out.PandasVersion != "2.2.3" || out.Rows != 4 {
			t.Fatalf("unexpected pandas output: %+v", out)
		}
		// Verify written outputs exist on host workspace view
		summaryBytes, err := os.ReadFile(filepath.Join(ws, "out", "summary.csv"))
		contractcheck.FailErr(t, "read host out/summary.csv", err)
		if !strings.Contains(string(summaryBytes), "gadget,2,0,6,150.0") {
			t.Fatalf("unexpected host summary.csv:\n%s", string(summaryBytes))
		}
		joinedBytes, err := os.ReadFile(filepath.Join(ws, "out", "joined.csv"))
		contractcheck.FailErr(t, "read host out/joined.csv", err)
		if !strings.Contains(string(joinedBytes), "0001,0010,2,widget") || !strings.Contains(string(joinedBytes), "0004,0099,3,") {
			t.Fatalf("unexpected host joined.csv:\n%s", string(joinedBytes))
		}
		// Verify ordinary file audit receipts were recorded
		var receiptCount int
		err = filepath.WalkDir(receiptDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr == nil && !d.IsDir() {
				receiptCount++
			}
			return walkErr
		})
		contractcheck.FailErr(t, "walk receipts", err)
		if receiptCount == 0 {
			t.Fatalf("expected file audit receipts in %s, got 0", receiptDir)
		}
		summary.PandasImportMS = out.ImportMS
		summary.PandasPeakMemPages = resp.Usage.PeakMemoryPages
		t.Logf("pandas 2.2.3 import=%.2fms wall=%dms peak_pages=%d work=%d receipts=%d",
			out.ImportMS, resp.Usage.Duration.Milliseconds(), resp.Usage.PeakMemoryPages, resp.Usage.GuestWork, receiptCount)
	})

	t.Run("03_kernel_cancel_fuel_and_fresh_recovery", func(t *testing.T) {
		ws := t.TempDir()
		roots := []pythondocuments.HostRootConfig{{ID: "ws", Dir: ws}}
		kernelCode := `
import numpy as np
A = np.ones((128, 128), dtype=np.float64)
B = np.ones((128, 128), dtype=np.float64)
while True:
    C = A @ B
`
		// 3a. Host cancellation inside numerical kernel
		cancelCtx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(150 * time.Millisecond)
			cancel()
		}()
		t0 := time.Now()
		respCancel, err := svc.ExecuteTool(cancelCtx, portablepython.ToolRequest{
			ExecutionID:  "py13-kernel-cancel",
			CallID:       "py13-kernel-cancel-call",
			SessionID:    "py13-numeric-qual",
			Actor:        "coordinator",
			View:         "primary",
			Surface:      "implement_investigate",
			RawArgs:      map[string]any{"code": kernelCode},
			Roots:        roots,
			MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
		})
		elapsedCancel := time.Since(t0)
		contractcheck.FailErr(t, "execute kernel cancel", err)
		if respCancel.CleanCompletion {
			t.Fatalf("expected non-clean stop on kernel cancel, got CleanCompletion=true")
		}
		if respCancel.Stop == nil || respCancel.Stop.Code != "PYTHON_CANCELLED" {
			t.Fatalf("expected stop code=PYTHON_CANCELLED, got %+v", respCancel.Stop)
		}
		summary.KernelCancelMS = float64(elapsedCancel.Milliseconds())

		// 3b. Fuel exhaustion inside numerical kernel
		respFuel, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			ExecutionID:  "py13-kernel-fuel",
			CallID:       "py13-kernel-fuel-call",
			SessionID:    "py13-numeric-qual",
			Actor:        "coordinator",
			View:         "primary",
			Surface:      "implement_investigate",
			RawArgs:      map[string]any{"code": kernelCode},
			Roots:        roots,
			MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
			Limits: pythonruntime.ResourceLimits{
				MaxGuestWork: 50_000_000,
			},
		})
		contractcheck.FailErr(t, "execute kernel fuel", err)
		if respFuel.CleanCompletion {
			t.Fatalf("expected fuel exhaustion stop, got CleanCompletion=true")
		}
		if respFuel.Stop == nil || respFuel.Stop.Code != "PYTHON_WORK_LIMIT_EXCEEDED" {
			t.Fatalf("expected stop code=PYTHON_WORK_LIMIT_EXCEEDED, got %+v", respFuel.Stop)
		}
		summary.KernelFuelExhausted = true

		// 3c. Subsequent fresh-guest success
		respFresh := runPY13Code(t, svc, ws, "py13-fresh-recovery", "import numpy as np; print(int(np.arange(5) @ np.arange(5)))")
		if strings.TrimSpace(respFresh.Stdout) != "30" {
			t.Fatalf("fresh recovery stdout=%q want 30", respFresh.Stdout)
		}
		summary.FreshRecoveryOK = true
		t.Logf("kernel cancel=%dms (reason=%s), fuel_exhausted work=%d, fresh recovery OK",
			elapsedCancel.Milliseconds(), respCancel.Stop.Reason, respFuel.Usage.GuestWork)
	})

	t.Run("04_stale_pair_refusal", func(t *testing.T) {
		m, err := pythonruntime.LoadBundledEngineManifest()
		contractcheck.FailErr(t, "load bundled manifest", err)

		// Pair A: PY13 engine manifest + tampered/stale PY10 image_id & wasm hash
		staleImgManifest := *m
		imgCopy := *m.Image
		imgCopy.ImageID = "pp-cpython-3.12.0-wasi-0a9050f3c8ee96b0"
		imgCopy.CPythonWasmSHA256 = "bea5b8f2cb633212d4be85a668ad13246dc0c1b65c29796cf5d990ec1956b052"
		imgCopy.MeteredWasmSHA256 = "92755437dfb30aa41a5868f06d585dac55990474e07ebca53d9e5b1f009758f4"
		staleImgManifest.Image = &imgCopy
		if err := pythonruntime.ValidateBundledEngineManifest(&staleImgManifest); err == nil {
			t.Fatalf("expected ValidateBundledEngineManifest to refuse PY13 engine + PY10 image stale pair")
		}

		// Pair B: PY10 engine wasm hashes + PY13 image
		staleEngManifest := *m
		staleEngManifest.Engine.CPythonWasmSHA256 = "bea5b8f2cb633212d4be85a668ad13246dc0c1b65c29796cf5d990ec1956b052"
		staleEngManifest.Engine.MeteredWasmSHA256 = "92755437dfb30aa41a5868f06d585dac55990474e07ebca53d9e5b1f009758f4"
		if err := pythonruntime.ValidateBundledEngineManifest(&staleEngManifest); err == nil {
			t.Fatalf("expected ValidateBundledEngineManifest to refuse PY10 engine + PY13 image stale pair")
		}

		// Also verify live staged directory refusal when image-manifest.json is swapped to a stale PY10 image_id
		stageRoot := os.Getenv("LYCAON_ENGINE_ROOT")
		if stageRoot != "" {
			if err := pythonruntime.VerifyStagedImageDir(&staleImgManifest, pythonruntime.StagedImageDir(stageRoot)); err == nil {
				t.Fatalf("expected VerifyStagedImageDir to refuse mismatched PY13 staged image against PY10 manifest")
			}
		}
		summary.StalePairRefusedBoth = true
	})

	t.Run("05_zero_host_binaries_in_staged_image", func(t *testing.T) {
		stageRoot := os.Getenv("LYCAON_ENGINE_ROOT")
		if stageRoot == "" {
			t.Skip("LYCAON_ENGINE_ROOT not set")
		}
		pkgsDir := filepath.Join(pythonruntime.StagedImageDir(stageRoot), "packages")
		forbidden := []string{".so", ".dylib", ".pyd", ".dll"}
		err := filepath.WalkDir(pkgsDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return walkErr
			}
			lower := strings.ToLower(d.Name())
			for _, ext := range forbidden {
				if strings.HasSuffix(lower, ext) {
					t.Errorf("forbidden host binary found in staged image: %s", path)
				}
			}
			return nil
		})
		contractcheck.FailErr(t, "walk staged packages dir", err)
	})

	rawSummary, _ := json.Marshal(summary)
	t.Logf("PY13_QUAL_SUMMARY=%s", string(rawSummary))
}
