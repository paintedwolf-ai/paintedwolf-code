package portablepython

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func py10FixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join("testdata", "py10", name)
	b, err := os.ReadFile(p)
	contractcheck.FailErr(t, "read py10 fixture "+name, err)
	return b
}

func copyPY10Fixture(t *testing.T, wsDir, name string) {
	t.Helper()
	dst := filepath.Join(wsDir, name)
	contractcheck.FailErr(t, "mkdir fixture parent", os.MkdirAll(filepath.Dir(dst), 0o755))
	contractcheck.FailErr(t, "write fixture "+name, os.WriteFile(dst, py10FixtureBytes(t, name), 0o644))
}

func newPY10Service(t *testing.T, receiptDir string) *portablepython.Service {
	t.Helper()
	requireStrict := os.Getenv("PW_REQUIRE_PY10") == "1" || os.Getenv("PW_REQUIRE_NATIVE_QUAL") == "1"
	if requireStrict {
		if strings.TrimSpace(os.Getenv(portablepython.EnvImageDir)) != "" {
			t.Fatalf("%s must be UNSET during PY10 qualification", portablepython.EnvImageDir)
		}
		if strings.TrimSpace(os.Getenv(pythonruntime.EnvEngineDir)) != "" {
			t.Fatalf("%s must be UNSET during PY10 qualification", pythonruntime.EnvEngineDir)
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
			t.Fatalf("failed to initialize PY10 Portable Python service: %v", err)
		}
		t.Skipf("no staged Portable Python service available (set LYCAON_ENGINE_ROOT): %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	info := svc.RuntimeInfo()
	if info.Backend != pythonruntime.BackendNativeAOT {
		t.Fatalf("PY10 default RuntimeInfo.Backend=%q want %q on %s/%s", info.Backend, pythonruntime.BackendNativeAOT, runtime.GOOS, runtime.GOARCH)
	}
	if info.NativeEngine == nil || info.NativeEngine.EngineTag == "" || info.NativeEngine.ABIVersion == 0 {
		t.Fatalf("PY10 RuntimeInfo.NativeEngine missing verified identity: %+v", info.NativeEngine)
	}
	return svc
}

func runPY10Code(t *testing.T, svc *portablepython.Service, wsDir, execID, code string) *portablepython.ToolResponse {
	t.Helper()
	roots := []pythondocuments.HostRootConfig{{ID: "ws", Dir: wsDir}}
	// Use unchanged default production limits (zero ResourceLimits -> Service defaults).
	resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
		ExecutionID:  execID,
		CallID:       execID + "-call",
		SessionID:    "py10-workflows",
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

func readZipEntries(t *testing.T, zipBytes []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	contractcheck.FailErr(t, "open zip archive", err)
	out := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		contractcheck.FailErr(t, "open zip member "+f.Name, err)
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		contractcheck.FailErr(t, "read zip member "+f.Name, err)
		out[f.Name] = b
	}
	return out
}

func maybeArchivePY10Artifact(name string, data []byte) {
	if dir := strings.TrimSpace(os.Getenv("PW_PY10_ARTIFACT_DIR")); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, name), data, 0o644)
	}
}

// TestPY10_NewPackageWorkflows exercises all 9 new PY10 distributions end-to-end
// through portablepython.Service on default native-aot with independent Go
// artifact oracles under unchanged production limits.
func TestPY10_NewPackageWorkflows(t *testing.T) {
	svc := newPY10Service(t, t.TempDir())

	t.Run("01_json5_commented_config_and_source_preservation", func(t *testing.T) {
		ws := t.TempDir()
		orig := py10FixtureBytes(t, "config.json5")
		copyPY10Fixture(t, ws, "config.json5")

		resp := runPY10Code(t, svc, ws, "py10-json5", `
import json, json5
from pathlib import Path

with open("config.json5", "r", encoding="utf-8") as f:
    cfg = json5.load(f)

assert cfg["service_url"] == "https://api.example.org/v1//items"
assert cfg["literal_comment"] == "// not a comment"
assert cfg["block_literal"] == "/* also not a comment */"
assert cfg["retries"] == 3
assert cfg["enabled"] is True
assert cfg["tags"] == ["alpha", "beta"]
Path("parsed.json").write_text(json.dumps(cfg, sort_keys=True), encoding="utf-8")
print("JSON5_OK")
`)
		if !strings.Contains(resp.Stdout, "JSON5_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}
		after, err := os.ReadFile(filepath.Join(ws, "config.json5"))
		contractcheck.FailErr(t, "read config.json5 after parse", err)
		if !bytes.Equal(orig, after) {
			t.Fatalf("config.json5 bytes mutated during read-only json5 parse")
		}
		parsedRaw, err := os.ReadFile(filepath.Join(ws, "parsed.json"))
		contractcheck.FailErr(t, "read parsed.json", err)
		var parsed map[string]any
		contractcheck.FailErr(t, "unmarshal parsed.json", json.Unmarshal(parsedRaw, &parsed))
		if parsed["service_url"] != "https://api.example.org/v1//items" {
			t.Fatalf("unexpected service_url: %v", parsed["service_url"])
		}
	})

	t.Run("02_tzdata_zoneinfo_folds_kathmandu_and_unknown", func(t *testing.T) {
		ws := t.TempDir()
		resp := runPY10Code(t, svc, ws, "py10-tzdata", `
import datetime as dt
import zoneinfo
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

# Force image-only tzdata (zero host tzdata search paths)
zoneinfo.reset_tzpath([])
assert zoneinfo.TZPATH == ()

ny = ZoneInfo("America/New_York")
ktm = ZoneInfo("Asia/Kathmandu")

# Autumn 2024 DST fall-back in New York: 2024-11-03 01:30 occurs twice
t_fold0 = dt.datetime(2024, 11, 3, 1, 30, tzinfo=ny, fold=0)
t_fold1 = dt.datetime(2024, 11, 3, 1, 30, tzinfo=ny, fold=1)
u0 = t_fold0.astimezone(dt.timezone.utc)
u1 = t_fold1.astimezone(dt.timezone.utc)
assert u0 == dt.datetime(2024, 11, 3, 5, 30, tzinfo=dt.timezone.utc), u0
assert u1 == dt.datetime(2024, 11, 3, 6, 30, tzinfo=dt.timezone.utc), u1
assert int((u1 - u0).total_seconds()) == 3600

# Asia/Kathmandu fixed +05:45 offset
t_ktm = dt.datetime(2024, 6, 15, 12, 0, tzinfo=ktm)
assert t_ktm.utcoffset() == dt.timedelta(hours=5, minutes=45)

try:
    ZoneInfo("Mars/Olympus_Mons")
    raise AssertionError("expected ZoneInfoNotFoundError")
except ZoneInfoNotFoundError:
    pass
print("TZDATA_OK")
`)
		if !strings.Contains(resp.Stdout, "TZDATA_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}
	})

	t.Run("03_python_dateutil_rrule_leap_dst_and_ambiguous_tz", func(t *testing.T) {
		ws := t.TempDir()
		resp := runPY10Code(t, svc, ws, "py10-dateutil", `
import datetime as dt
import warnings
import zoneinfo
from zoneinfo import ZoneInfo
from dateutil.rrule import rrule, MONTHLY, TU
from dateutil.relativedelta import relativedelta
from dateutil import parser as dtparser

zoneinfo.reset_tzpath([])

# 3rd Tuesday across Jan-Apr 2024
tuesdays = [
    d.date().isoformat()
    for d in rrule(MONTHLY, byweekday=TU(3), dtstart=dt.datetime(2024, 1, 1), count=4)
]
assert tuesdays == ["2024-01-16", "2024-02-20", "2024-03-19", "2024-04-16"], tuesdays

# Month-end leap-year calculation
assert dt.date(2024, 1, 31) + relativedelta(months=1) == dt.date(2024, 2, 29)
assert dt.date(2024, 2, 29) + relativedelta(years=1) == dt.date(2025, 2, 28)

# Retain local wall time across US spring DST transition (2024-03-10)
ny = ZoneInfo("America/New_York")
before_dst = dt.datetime(2024, 3, 9, 9, 0, tzinfo=ny)
after_dst = before_dst + relativedelta(days=1)
assert (after_dst.hour, after_dst.minute) == (9, 0)
assert before_dst.utcoffset() == dt.timedelta(hours=-5)
assert after_dst.utcoffset() == dt.timedelta(hours=-4)

# Ambiguous timezone abbreviation requires explicit tzinfos
with warnings.catch_warnings(record=True) as w:
    warnings.simplefilter("always")
    naive = dtparser.parse("2024-06-01 12:00 CST")
    assert naive.tzinfo is None
    assert any("tzname CST" in str(item.message) for item in w), [str(x.message) for x in w]

resolved = dtparser.parse("2024-06-01 12:00 CST", tzinfos={"CST": ZoneInfo("America/Chicago")})
assert resolved.tzinfo is not None and resolved.utcoffset() == dt.timedelta(hours=-5)
print("DATEUTIL_OK")
`)
		if !strings.Contains(resp.Stdout, "DATEUTIL_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}
	})

	t.Run("04_sympy_exact_rationals_diff_and_substitution", func(t *testing.T) {
		ws := t.TempDir()
		resp := runPY10Code(t, svc, ws, "py10-sympy", `
import sympy as sp

x, y = sp.symbols("x y")
sol = sp.solve([2 * x + 3 * y - 7, 5 * x - y - 9], (x, y), dict=True)
assert len(sol) == 1
assert sol[0][x] == sp.Rational(34, 17) == sp.Integer(2)
assert sol[0][y] == sp.Rational(17, 17) == sp.Integer(1)

poly_roots = sp.solve(x**2 - sp.Rational(5, 2) * x + 1, x)
assert set(poly_roots) == {sp.Rational(1, 2), sp.Integer(2)}, poly_roots

expr = sp.Rational(1, 3) * x**3 - 2 * x**2 + 5 * x
deriv = sp.diff(expr, x)
val_at_3_2 = deriv.subs(x, sp.Rational(3, 2))
assert val_at_3_2 == sp.Rational(5, 4), val_at_3_2
# Substitute back into original expression
orig_val = expr.subs(x, sp.Rational(3, 2))
assert orig_val == sp.Rational(33, 8), orig_val
print("SYMPY_OK", sp.__version__)
`)
		if !strings.Contains(resp.Stdout, "SYMPY_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}
	})

	t.Run("05_pillow_exif_transpose_rgba_bytesio_corrupt_and_bomb", func(t *testing.T) {
		ws := t.TempDir()
		copyPY10Fixture(t, ws, "exif_orient6.jpg")
		copyPY10Fixture(t, ws, "sample.png")
		copyPY10Fixture(t, ws, "corrupt.jpg")

		resp := runPY10Code(t, svc, ws, "py10-pillow", `
import io
from PIL import Image, ImageOps, UnidentifiedImageError

# 1. JPEG EXIF Orientation=6 transpose + resize -> PNG
with Image.open("exif_orient6.jpg") as im:
    assert im.size == (60, 40), im.size
    exif = im.getexif()
    assert exif.get(274) == 6, dict(exif)
    transposed = ImageOps.exif_transpose(im)
    assert transposed.size == (40, 60), transposed.size
    resized = transposed.resize((80, 120), Image.Resampling.NEAREST)
    resized.save("transposed.png", format="PNG")

# 2. PNG RGBA transparency + BytesIO round-trip
with Image.open("sample.png") as rgba:
    assert rgba.mode == "RGBA" and rgba.size == (32, 32)
    assert rgba.getpixel((24, 8))[3] == 128
    buf = io.BytesIO()
    rgba.save(buf, format="PNG")
    buf.seek(0)
    with Image.open(buf) as rt:
        assert rt.mode == "RGBA" and rt.getpixel((24, 8))[3] == 128

# 3. Invalid/corrupt image raises UnidentifiedImageError
try:
    with Image.open("corrupt.jpg") as _:
        raise AssertionError("corrupt.jpg should fail")
except UnidentifiedImageError:
    pass

# 4. Oversized-image DecompressionBombError refusal
orig_max = Image.MAX_IMAGE_PIXELS
try:
    Image.MAX_IMAGE_PIXELS = 500
    try:
        with Image.open("sample.png") as im:
            im.load()
        raise AssertionError("expected DecompressionBombError for 32x32=1024 > 500")
    except Image.DecompressionBombError:
        pass
finally:
    Image.MAX_IMAGE_PIXELS = orig_max
print("PILLOW_OK")
`)
		if !strings.Contains(resp.Stdout, "PILLOW_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}

		// Independent Go image/png oracle: verify exact dimensions (80x120) and 4 rotated quadrants.
		pngBytes, err := os.ReadFile(filepath.Join(ws, "transposed.png"))
		contractcheck.FailErr(t, "read transposed.png", err)
		maybeArchivePY10Artifact("transposed.png", pngBytes)

		decoded, err := png.Decode(bytes.NewReader(pngBytes))
		contractcheck.FailErr(t, "decode transposed.png", err)
		b := decoded.Bounds()
		if b.Dx() != 80 || b.Dy() != 120 {
			t.Fatalf("transposed.png dimensions = %dx%d, want 80x120", b.Dx(), b.Dy())
		}
		checkRGB := func(label string, x, y int, wantR, wantG, wantB bool) {
			r32, g32, b32, _ := decoded.At(x, y).RGBA()
			r, g, bl := uint8(r32>>8), uint8(g32>>8), uint8(b32>>8)
			ok := func(v uint8, high bool) bool {
				if high {
					return v >= 170
				}
				return v <= 85
			}
			if !ok(r, wantR) || !ok(g, wantG) || !ok(bl, wantB) {
				t.Fatalf("quadrant %s at (%d,%d) got RGB(%d,%d,%d), want high=(%v,%v,%v)", label, x, y, r, g, bl, wantR, wantG, wantB)
			}
		}
		// Raw (60x40): TL=Red, TR=Green, BL=Blue, BR=Yellow.
		// After 90 deg CW (40x60 -> 80x120): TL=Blue, TR=Red, BL=Yellow, BR=Green.
		checkRGB("TL_Blue", 20, 30, false, false, true)
		checkRGB("TR_Red", 60, 30, true, false, false)
		checkRGB("BL_Yellow", 20, 90, true, true, false)
		checkRGB("BR_Green", 60, 90, false, true, false)
	})

	t.Run("06_lxml_xpath_office_xml_and_zero_external_entity_access", func(t *testing.T) {
		ws := t.TempDir()
		copyPY10Fixture(t, ws, "namespaced.xml")
		copyPY10Fixture(t, ws, "input_report.docx")

		var httpHits atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpHits.Add(1)
			_, _ = w.Write([]byte("HTTP_XXE_SHOULD_NEVER_BE_FETCHED"))
		}))
		defer srv.Close()
		port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")

		xxeXML := strings.ReplaceAll(string(py10FixtureBytes(t, "external_entity.xml")), "__PORT__", port)
		contractcheck.FailErr(t, "write external_entity.xml", os.WriteFile(filepath.Join(ws, "external_entity.xml"), []byte(xxeXML), 0o644))
		contractcheck.FailErr(t, "write secret_xxe.txt", os.WriteFile(filepath.Join(ws, "secret_xxe.txt"), []byte("XXE_SECRET_NEVER_READ"), 0o644))

		resp := runPY10Code(t, svc, ws, "py10-lxml", `
import zipfile
from lxml import etree

# 1. Namespaced XPath query
doc = etree.parse("namespaced.xml")
ns = {"inv": "urn:pw:inventory", "meta": "urn:pw:meta"}
gold = doc.xpath("//inv:item[@meta:tier='gold']/inv:name/text()", namespaces=ns)
assert gold == ["Widget Alpha", "Widget Gamma"], gold

# 2. Local in-memory parser + Office XML
with zipfile.ZipFile("input_report.docx", "r") as zf:
    wdoc = etree.fromstring(zf.read("word/document.xml"))
wns = {"w": "http://schemas.openxmlformats.org/wordprocessingml/2006/main"}
texts = wdoc.xpath("//w:t/text()", namespaces=wns)
assert "Quarterly Inventory Report" in texts and "DRAFT" in texts, texts

# 3. External entity / DTD fixture with safe parser (zero network or file fetch)
safe_parser = etree.XMLParser(resolve_entities=False, no_network=True, load_dtd=False)
xxe_doc = etree.parse("external_entity.xml", parser=safe_parser)
serialized = etree.tostring(xxe_doc, encoding="unicode")
assert "XXE_SECRET_NEVER_READ" not in serialized
assert "HTTP_XXE_SHOULD_NEVER_BE_FETCHED" not in serialized
print("LXML_OK")
`)
		if !strings.Contains(resp.Stdout, "LXML_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}
		if httpHits.Load() != 0 {
			t.Fatalf("lxml external entity fixture triggered %d unapproved HTTP fetches, want 0", httpHits.Load())
		}
		if resp.AdmissionEvidence != nil {
			for _, rec := range resp.AdmissionEvidence.Admissions() {
				if rec.Target.RelPath == "secret_xxe.txt" {
					t.Fatalf("lxml external entity fixture attempted unapproved read of secret_xxe.txt: %+v", rec)
				}
			}
		}
	})

	t.Run("07_python_docx_edit_table_add_paragraph_png_and_ooxml_oracle", func(t *testing.T) {
		ws := t.TempDir()
		copyPY10Fixture(t, ws, "input_report.docx")
		copyPY10Fixture(t, ws, "sample.png")

		resp := runPY10Code(t, svc, ws, "py10-docx", `
from docx import Document
from docx.shared import Inches

doc = Document("input_report.docx")
assert any("Quarterly Inventory Report" in p.text for p in doc.paragraphs)
tbl = doc.tables[0]
assert tbl.cell(1, 1).text.strip() == "DRAFT"
tbl.cell(1, 1).text = "APPROVED"
doc.add_paragraph("Verified by PY10 native-aot workflow.")
doc.add_picture("sample.png", width=Inches(1.0))
doc.save("output_report.docx")

# Reopen in Python
doc2 = Document("output_report.docx")
assert doc2.tables[0].cell(1, 1).text.strip() == "APPROVED"
assert any("Verified by PY10 native-aot workflow." in p.text for p in doc2.paragraphs)
assert len(doc2.inline_shapes) == 1
print("DOCX_OK")
`)
		if !strings.Contains(resp.Stdout, "DOCX_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}

		// Independent Go OOXML oracle
		outBytes, err := os.ReadFile(filepath.Join(ws, "output_report.docx"))
		contractcheck.FailErr(t, "read output_report.docx", err)
		maybeArchivePY10Artifact("output_report.docx", outBytes)

		entries := readZipEntries(t, outBytes)
		docXML := string(entries["word/document.xml"])
		if !strings.Contains(docXML, "Quarterly Inventory Report") || !strings.Contains(docXML, "APPROVED") || strings.Contains(docXML, "DRAFT") {
			t.Fatalf("output_report.docx word/document.xml table update mismatch")
		}
		if !strings.Contains(docXML, "Verified by PY10 native-aot workflow.") {
			t.Fatalf("output_report.docx missing added paragraph")
		}
		relsXML := string(entries["word/_rels/document.xml.rels"])
		if !strings.Contains(relsXML, "relationships/image") {
			t.Fatalf("output_report.docx missing image relationship in document.xml.rels")
		}
		foundPNG := false
		for name, data := range entries {
			if strings.HasPrefix(name, "word/media/") && bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
				foundPNG = true
				break
			}
		}
		if !foundPNG {
			t.Fatalf("output_report.docx missing valid PNG bytes in word/media/")
		}
	})

	t.Run("08_python_pptx_two_slides_image_native_chart_and_ooxml_oracle", func(t *testing.T) {
		ws := t.TempDir()
		copyPY10Fixture(t, ws, "chart_data.json")
		copyPY10Fixture(t, ws, "sample.png")

		resp := runPY10Code(t, svc, ws, "py10-pptx", `
import json
from pptx import Presentation
from pptx.chart.data import CategoryChartData
from pptx.enum.chart import XL_CHART_TYPE
from pptx.util import Inches

with open("chart_data.json", "r", encoding="utf-8") as f:
    data = json.load(f)

prs = Presentation()
# Slide 1: title + body + PNG
s1 = prs.slides.add_slide(prs.slide_layouts[1])
s1.shapes.title.text = "PY10 Executive Summary"
s1.placeholders[1].text = "Generated inside Portable Python native-aot."
s1.shapes.add_picture("sample.png", Inches(6.0), Inches(2.0), width=Inches(1.5))

# Slide 2: title + native chart
s2 = prs.slides.add_slide(prs.slide_layouts[5])
s2.shapes.title.text = data["title"]
chart_data = CategoryChartData()
chart_data.categories = data["categories"]
for s in data["series"]:
    chart_data.add_series(s["name"], s["values"])
s2.shapes.add_chart(
    XL_CHART_TYPE.COLUMN_CLUSTERED,
    Inches(1.0), Inches(1.8), Inches(7.5), Inches(4.5),
    chart_data,
)
prs.save("deck.pptx")

# Reopen in Python
prs2 = Presentation("deck.pptx")
assert len(prs2.slides) == 2
assert prs2.slides[0].shapes.title.text == "PY10 Executive Summary"
assert prs2.slides[1].shapes.title.text == "Quarterly Throughput"
print("PPTX_OK")
`)
		if !strings.Contains(resp.Stdout, "PPTX_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}

		// Independent Go OOXML oracle
		deckBytes, err := os.ReadFile(filepath.Join(ws, "deck.pptx"))
		contractcheck.FailErr(t, "read deck.pptx", err)
		maybeArchivePY10Artifact("deck.pptx", deckBytes)

		entries := readZipEntries(t, deckBytes)
		for _, req := range []string{"ppt/presentation.xml", "ppt/slides/slide1.xml", "ppt/slides/slide2.xml", "ppt/charts/chart1.xml"} {
			if _, ok := entries[req]; !ok {
				t.Fatalf("deck.pptx missing required OOXML part %s", req)
			}
		}
		if !strings.Contains(string(entries["ppt/slides/slide1.xml"]), "PY10 Executive Summary") {
			t.Fatalf("slide1.xml missing expected title")
		}
		chartXML := string(entries["ppt/charts/chart1.xml"])
		for _, token := range []string{"Native AOT", "Baseline", "120", "190", "38"} {
			if !strings.Contains(chartXML, token) {
				t.Fatalf("chart1.xml missing expected series/value %q", token)
			}
		}
		foundXLSX, foundPNG := false, false
		for name, data := range entries {
			if strings.HasPrefix(name, "ppt/embeddings/") && strings.HasSuffix(name, ".xlsx") && bytes.HasPrefix(data, []byte("PK\x03\x04")) {
				foundXLSX = true
			}
			if strings.HasPrefix(name, "ppt/media/") && bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
				foundPNG = true
			}
		}
		if !foundXLSX {
			t.Fatalf("deck.pptx missing embedded chart workbook under ppt/embeddings/*.xlsx")
		}
		if !foundPNG {
			t.Fatalf("deck.pptx missing PNG media under ppt/media/")
		}
	})

	t.Run("09_fpdf2_two_page_unicode_table_image_and_pypdf_oracle", func(t *testing.T) {
		ws := t.TempDir()
		copyPY10Fixture(t, ws, "sample.png")

		resp := runPY10Code(t, svc, ws, "py10-fpdf2", `
from pathlib import Path
from fpdf import FPDF
from pypdf import PdfReader

# Discover pinned redistributable Unicode TTF font bundled in the image
font_path = Path("/packages/fonts/DejaVuSans.ttf")
if not font_path.is_file():
    font_candidates = sorted(Path("/packages/fonts").glob("*.ttf"))
    font_path = font_candidates[0] if font_candidates else None
assert font_path is not None and font_path.is_file(), "no bundled Unicode .ttf font found in /packages/fonts"

pdf = FPDF()
pdf.set_auto_page_break(auto=True, margin=15)
pdf.add_font("BundledUni", "", str(font_path))

# Page 1: Unicode heading, table, and PNG
pdf.add_page()
pdf.set_font("BundledUni", size=16)
pdf.cell(0, 10, "PY10 Report · Café · €123.45", new_x="LMARGIN", new_y="NEXT")
pdf.set_font("BundledUni", size=11)
rows = [("Item", "Status", "Total"), ("Widget Alpha", "OK", "€45.00"), ("Widget Gamma", "OK", "€78.45")]
for r in rows:
    pdf.cell(60, 8, r[0], border=1)
    pdf.cell(40, 8, r[1], border=1)
    pdf.cell(40, 8, r[2], border=1, new_x="LMARGIN", new_y="NEXT")
pdf.ln(4)
pdf.image("sample.png", x=15, y=70, w=25)

# Page 2: Explicit measured second page
pdf.add_page()
pdf.set_font("BundledUni", size=13)
pdf.cell(0, 10, "Page 2 · Summary & Verification → Complete", new_x="LMARGIN", new_y="NEXT")
pdf.output("report.pdf")

# Verify text and page count with pypdf
reader = PdfReader("report.pdf")
assert len(reader.pages) == 2, len(reader.pages)
p1_text = reader.pages[0].extract_text() or ""
p2_text = reader.pages[1].extract_text() or ""
assert "PY10 Report" in p1_text and "Caf" in p1_text, p1_text
assert "Page 2" in p2_text and "Complete" in p2_text, p2_text
print("FPDF2_OK", font_path.name)
`)
		if !strings.Contains(resp.Stdout, "FPDF2_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}
		pdfBytes, err := os.ReadFile(filepath.Join(ws, "report.pdf"))
		contractcheck.FailErr(t, "read report.pdf", err)
		maybeArchivePY10Artifact("report.pdf", pdfBytes)
		if !bytes.HasPrefix(pdfBytes, []byte("%PDF-")) {
			t.Fatalf("report.pdf missing %%PDF- header")
		}
		if bytes.Count(pdfBytes, []byte("/Type /Page")) < 2 {
			t.Fatalf("report.pdf expected at least 2 /Type /Page markers")
		}
	})
}

// TestPY10_BaselinePackageExamples exercises the 4 already-installed baseline
// capabilities documented in PY10: Pygments, charset-normalizer, sqlite3, and
// openpyxl's chart API.
func TestPY10_BaselinePackageExamples(t *testing.T) {
	svc := newPY10Service(t, t.TempDir())

	t.Run("10_pygments_mixed_language_tokenization", func(t *testing.T) {
		ws := t.TempDir()
		copyPY10Fixture(t, ws, "mixed_snippet.py")
		copyPY10Fixture(t, ws, "mixed_query.sql")

		resp := runPY10Code(t, svc, ws, "py10-pygments", `
from pathlib import Path
from pygments import lex
from pygments.lexers import PythonLexer, SqlLexer
from pygments.token import Token

py_tokens = list(lex(Path("mixed_snippet.py").read_text(encoding="utf-8"), PythonLexer()))
sql_tokens = list(lex(Path("mixed_query.sql").read_text(encoding="utf-8"), SqlLexer()))

assert any(tok == Token.Keyword and val == "def" for tok, val in py_tokens)
assert any(tok == Token.Name.Function and val == "compute_total" for tok, val in py_tokens)
assert any(tok == Token.Comment.Single and "sum positive" in val for tok, val in py_tokens)
assert any(tok == Token.Keyword and val.upper() == "SELECT" for tok, val in sql_tokens)
assert any(tok == Token.Keyword and val.upper() == "GROUP" for tok, val in sql_tokens)
print("PYGMENTS_OK", len(py_tokens), len(sql_tokens))
`)
		if !strings.Contains(resp.Stdout, "PYGMENTS_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}
	})

	t.Run("11_charset_normalizer_legacy_cp1252_and_ambiguous_control", func(t *testing.T) {
		ws := t.TempDir()
		copyPY10Fixture(t, ws, "legacy_cp1252.txt")
		copyPY10Fixture(t, ws, "ambiguous_short.bin")

		resp := runPY10Code(t, svc, ws, "py10-charset", `
from pathlib import Path
import charset_normalizer

raw = Path("legacy_cp1252.txt").read_bytes()
matches = charset_normalizer.from_bytes(raw)
best = matches.best()
assert best is not None
candidates = [m.encoding for m in matches]
assert best.encoding in ("cp1252", "windows_1252") or "cp1252" in candidates, (best.encoding, candidates)
decoded = raw.decode("cp1252")
assert "Résumé des résultats" in decoded and "€123.45" in decoded

# Ambiguous/tiny ASCII control
short_matches = charset_normalizer.from_bytes(Path("ambiguous_short.bin").read_bytes())
short_best = short_matches.best()
assert short_best is not None and str(short_best) == "ok\n"
print("CHARSET_OK", best.encoding)
`)
		if !strings.Contains(resp.Stdout, "CHARSET_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}
	})

	t.Run("12_sqlite3_csv_jsonl_in_memory_join", func(t *testing.T) {
		ws := t.TempDir()
		copyPY10Fixture(t, ws, "orders.csv")
		copyPY10Fixture(t, ws, "customers.jsonl")

		resp := runPY10Code(t, svc, ws, "py10-sqlite3", `
import csv, json, sqlite3
from pathlib import Path

conn = sqlite3.connect(":memory:")
conn.execute("CREATE TABLE customers (id TEXT PRIMARY KEY, region TEXT, name TEXT)")
conn.execute("CREATE TABLE orders (order_id INTEGER PRIMARY KEY, customer_id TEXT, amount INTEGER)")

for line in Path("customers.jsonl").read_text(encoding="utf-8").splitlines():
    row = json.loads(line)
    conn.execute("INSERT INTO customers VALUES (?, ?, ?)", (row["id"], row["region"], row["name"]))

with open("orders.csv", "r", encoding="utf-8", newline="") as f:
    for row in csv.DictReader(f):
        conn.execute("INSERT INTO orders VALUES (?, ?, ?)", (int(row["order_id"]), row["customer_id"], int(row["amount"])))

totals = dict(conn.execute(
    "SELECT c.region, SUM(o.amount) FROM orders o JOIN customers c ON c.id = o.customer_id GROUP BY c.region ORDER BY c.region"
).fetchall())
Path("region_totals.json").write_text(json.dumps(totals, sort_keys=True), encoding="utf-8")
print("SQLITE3_OK", totals)
`)
		if !strings.Contains(resp.Stdout, "SQLITE3_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}
		gotRaw, err := os.ReadFile(filepath.Join(ws, "region_totals.json"))
		contractcheck.FailErr(t, "read region_totals.json", err)
		var totals map[string]int
		contractcheck.FailErr(t, "unmarshal region_totals.json", json.Unmarshal(gotRaw, &totals))
		if totals["east"] != 75 || totals["west"] != 175 {
			t.Fatalf("unexpected sqlite3 join totals: %+v", totals)
		}
	})

	t.Run("13_openpyxl_chart_api_and_series_reference_oracle", func(t *testing.T) {
		ws := t.TempDir()
		copyPY10Fixture(t, ws, "chart_data.json")

		resp := runPY10Code(t, svc, ws, "py10-openpyxl-chart", `
import json
from openpyxl import Workbook
from openpyxl.chart import BarChart, Reference

with open("chart_data.json", "r", encoding="utf-8") as f:
    data = json.load(f)

wb = Workbook()
ws = wb.active
ws.title = "Data"
ws.append(["Quarter"] + [s["name"] for s in data["series"]])
for idx, cat in enumerate(data["categories"]):
    ws.append([cat] + [s["values"][idx] for s in data["series"]])

chart = BarChart()
chart.title = data["title"]
cats = Reference(ws, min_col=1, min_row=2, max_row=5)
vals = Reference(ws, min_col=2, max_col=3, min_row=1, max_row=5)
chart.add_data(vals, titles_from_data=True)
chart.set_categories(cats)
ws.add_chart(chart, "E2")
wb.save("throughput_chart.xlsx")
print("OPENPYXL_CHART_OK")
`)
		if !strings.Contains(resp.Stdout, "OPENPYXL_CHART_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}
		xlsxBytes, err := os.ReadFile(filepath.Join(ws, "throughput_chart.xlsx"))
		contractcheck.FailErr(t, "read throughput_chart.xlsx", err)
		maybeArchivePY10Artifact("throughput_chart.xlsx", xlsxBytes)

		entries := readZipEntries(t, xlsxBytes)
		chartXML := string(entries["xl/charts/chart1.xml"])
		if chartXML == "" {
			t.Fatalf("throughput_chart.xlsx missing xl/charts/chart1.xml")
		}
		for _, want := range []string{"Quarterly Throughput", "Data", "B1", "$B$2:$B$5", "C1", "$C$2:$C$5", "$A$2:$A$5"} {
			if !strings.Contains(chartXML, want) {
				t.Fatalf("xl/charts/chart1.xml missing expected reference %q", want)
			}
		}
	})
}

// TestPY10_AuthoritySettlementCancelAndLimits verifies library I/O against
// unsaved editor drafts, denied destinations, exception rollback, mid-flight
// cancellation, and fuel-limit recovery on default native-aot.
func TestPY10_AuthoritySettlementCancelAndLimits(t *testing.T) {
	svc := newPY10Service(t, t.TempDir())

	t.Run("unsaved_editor_input_reflected_in_library_save", func(t *testing.T) {
		ws := t.TempDir()
		copyPY10Fixture(t, ws, "chart_data.json")
		draftData := []byte(`{"title":"Draft Title","categories":["Q1"],"series":[{"name":"UnsavedDraft","values":[999]}]}`)
		editor := &qualEditorBridge{
			drafts: map[string]pythondocuments.EditorDraft{
				"chart_data.json": {
					DocumentID: "doc-chart-data",
					Revision:   "rev-draft-9",
					Bytes:      draftData,
					BaseSHA256: pythondocuments.SHA256Hex(draftData),
					Dirty:      true,
				},
			},
		}
		roots := []pythondocuments.HostRootConfig{{ID: "ws", Dir: ws}}
		resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			ExecutionID:  "py10-unsaved-draft",
			CallID:       "py10-unsaved-draft-call",
			SessionID:    "py10-controls",
			Actor:        "coordinator",
			View:         "primary",
			Surface:      "implement_investigate",
			RawArgs: map[string]any{"code": `
import json
from openpyxl import Workbook
data = json.load(open("chart_data.json", "r", encoding="utf-8"))
wb = Workbook()
ws = wb.active
ws["A1"] = data["title"]
ws["B1"] = data["series"][0]["values"][0]
wb.save("from_draft.xlsx")
`},
			Roots:        roots,
			Editor:       editor,
			MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
		})
		contractcheck.FailErr(t, "execute unsaved draft library save", err)
		if !resp.CleanCompletion || resp.ExitCode != 0 {
			t.Fatalf("unsaved draft run failed: exit=%d stderr=%q", resp.ExitCode, resp.Stderr)
		}
		xlsxBytes, err := os.ReadFile(filepath.Join(ws, "from_draft.xlsx"))
		contractcheck.FailErr(t, "read from_draft.xlsx", err)
		entries := readZipEntries(t, xlsxBytes)
		sheetXML := string(entries["xl/worksheets/sheet1.xml"])
		if !strings.Contains(sheetXML, "999") {
			t.Fatalf("from_draft.xlsx did not reflect unsaved editor draft value 999: %s", sheetXML)
		}
	})

	t.Run("denied_destination_blocks_library_save", func(t *testing.T) {
		ws := t.TempDir()
		roots := []pythondocuments.HostRootConfig{{ID: "ws", Dir: ws}}
		resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			ExecutionID: "py10-denied-save",
			CallID:      "py10-denied-save-call",
			SessionID:   "py10-controls",
			Actor:       "coordinator",
			View:        "primary",
			Surface:     "implement_investigate",
			RawArgs: map[string]any{"code": `
from openpyxl import Workbook
wb = Workbook()
wb.active["A1"] = "should-not-write"
wb.save("denied_out.xlsx")
`},
			Roots:        roots,
			MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
			Policy: func(_ context.Context, op pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
				if op.Target.RelPath == "denied_out.xlsx" {
					return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionDeny, Reason: "py10_denied_dest"}
				}
				return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
			},
		})
		contractcheck.FailErr(t, "execute denied library save", err)
		if resp.CleanCompletion {
			t.Fatalf("denied library save unexpectedly succeeded cleanly")
		}
		if _, statErr := os.Stat(filepath.Join(ws, "denied_out.xlsx")); !os.IsNotExist(statErr) {
			t.Fatalf("denied_out.xlsx must not exist on disk after denial")
		}
		sawDeny := false
		if resp.AdmissionEvidence != nil {
			for _, rec := range resp.AdmissionEvidence.Admissions() {
				if rec.Decision == pythonruntime.DecisionDeny && rec.Target.RelPath == "denied_out.xlsx" {
					sawDeny = true
				}
			}
		}
		if !sawDeny {
			t.Fatalf("expected DecisionDeny admission record for denied_out.xlsx")
		}
	})

	t.Run("exception_before_settlement_rolls_back_library_write", func(t *testing.T) {
		ws := t.TempDir()
		roots := []pythondocuments.HostRootConfig{{ID: "ws", Dir: ws}}
		resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			ExecutionID: "py10-rollback-exc",
			CallID:      "py10-rollback-exc-call",
			SessionID:   "py10-controls",
			Actor:       "coordinator",
			View:        "primary",
			Surface:     "implement_investigate",
			RawArgs: map[string]any{"code": `
from openpyxl import Workbook
wb = Workbook()
wb.active["A1"] = "staged-then-aborted"
wb.save("aborted.xlsx")
raise RuntimeError("py10 boom before settlement")
`},
			Roots:        roots,
			MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
		})
		contractcheck.FailErr(t, "execute exception rollback", err)
		if resp.CleanCompletion || resp.ExitCode == 0 {
			t.Fatalf("expected non-clean failure on uncaught exception, got clean=%v exit=%d", resp.CleanCompletion, resp.ExitCode)
		}
		if resp.Settlement != nil && len(resp.Settlement.Published) != 0 {
			t.Fatalf("expected 0 published writes after exception rollback, got %d", len(resp.Settlement.Published))
		}
		if _, statErr := os.Stat(filepath.Join(ws, "aborted.xlsx")); !os.IsNotExist(statErr) {
			t.Fatalf("aborted.xlsx must not exist on disk after exception rollback")
		}
	})

	t.Run("cancel_long_native_op_and_fuel_limit_recovery", func(t *testing.T) {
		ws := t.TempDir()
		roots := []pythondocuments.HostRootConfig{{ID: "ws", Dir: ws}}

		// 1. Mid-flight cancellation of CPU-bound guest
		cancelCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		start := time.Now()
		respCancel, err := svc.ExecuteTool(cancelCtx, portablepython.ToolRequest{
			ExecutionID:  "py10-cancel-cpu",
			CallID:       "py10-cancel-cpu-call",
			SessionID:    "py10-controls",
			Actor:        "coordinator",
			View:         "primary",
			Surface:      "implement_investigate",
			RawArgs:      map[string]any{"code": "x = 0\nwhile True:\n    x += 1\n"},
			Roots:        roots,
			MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
		})
		elapsed := time.Since(start)
		if err == nil && respCancel != nil && respCancel.CleanCompletion {
			t.Fatalf("infinite loop should not complete cleanly")
		}
		if elapsed > 5*time.Second {
			t.Fatalf("cancellation took too long (%v); expected prompt release", elapsed)
		}

		// 2. Deterministic fuel limit exhaustion
		respFuel, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			ExecutionID:  "py10-fuel-limit",
			CallID:       "py10-fuel-limit-call",
			SessionID:    "py10-controls",
			Actor:        "coordinator",
			View:         "primary",
			Surface:      "implement_investigate",
			RawArgs:      map[string]any{"code": "s = 0\nfor i in range(10_000_000):\n    s += i\n"},
			Roots:        roots,
			MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
			Limits:       pythonruntime.ResourceLimits{MaxGuestWork: 20_000_000},
		})
		contractcheck.FailErr(t, "execute fuel limit test", err)
		if respFuel.CleanCompletion || respFuel.Stop == nil || respFuel.Stop.Code != pythonruntime.StopWorkLimitExceeded {
			t.Fatalf("expected %s, got clean=%v stop=%+v", pythonruntime.StopWorkLimitExceeded, respFuel.CleanCompletion, respFuel.Stop)
		}

		// 3. Subsequent fresh guest on the SAME Service succeeds cleanly
		respNext := runPY10Code(t, svc, ws, "py10-post-recovery", `print("OK_AFTER_RECOVERY")`)
		if !strings.Contains(respNext.Stdout, "OK_AFTER_RECOVERY") {
			t.Fatalf("post-recovery guest unexpected stdout: %q", respNext.Stdout)
		}
		t.Logf("PASS cancel (%v) + fuel limit (%s) + fresh guest recovery", elapsed, respFuel.Stop.Code)
	})
}

// Keep fmt imported cleanly if needed in future expansions.
var _ = fmt.Sprintf
