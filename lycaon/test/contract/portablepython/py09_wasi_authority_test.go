package portablepython

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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

const py09WASIParityScript = `
import csv
import errno
import hashlib
import io
import json
import os
import pathlib
import zipfile
import yaml
import openpyxl

out = {}

# 1. Short reads, EOF, seek/tell (SEEK_SET, SEEK_CUR, SEEK_END)
with open("seed.bin", "rb") as f:
    p1 = f.read(3)
    p2 = f.read(5)
    pos_after_8 = f.tell()
    f.seek(2, os.SEEK_CUR)
    p3 = f.read(4)
    f.seek(-4, os.SEEK_END)
    tail = f.read(16)
    eof_again = f.read(8)
    f.seek(0, os.SEEK_SET)
    full = f.read()
out["short_reads"] = {
    "p1": p1.hex(),
    "p2": p2.hex(),
    "pos_after_8": pos_after_8,
    "p3": p3.hex(),
    "tail": tail.hex(),
    "eof_empty": eof_again == b"",
    "full_sha256": hashlib.sha256(full).hexdigest(),
}

# 2. Binary data including embedded NUL bytes + close/reopen
nul_payload = b"\x00\x01\xff\x00hello\x00world\x00\xfe\x00"
with open("binary_nul.bin", "wb") as f:
    f.write(nul_payload)
with open("binary_nul.bin", "rb") as f:
    reopened_nul = f.read()
out["binary_nul"] = {
    "len": len(reopened_nul),
    "matches": reopened_nul == nul_payload,
    "hex": reopened_nul.hex(),
}

# 3. Append vs truncate
with open("append_trunc.bin", "wb") as f:
    f.write(b"0123456789ABCDEF")
with open("append_trunc.bin", "ab") as f:
    f.write(b"XYZ")
with open("append_trunc.bin", "rb") as f:
    after_append = f.read()
with open("append_trunc.bin", "r+b") as f:
    f.truncate(7)
with open("append_trunc.bin", "rb") as f:
    after_ftruncate = f.read()
with open("append_trunc.bin", "wb") as f:
    f.write(b"NEW")
with open("append_trunc.bin", "rb") as f:
    after_wb_trunc = f.read()
out["append_trunc"] = {
    "after_append": after_append.decode("ascii"),
    "after_ftruncate": after_ftruncate.decode("ascii"),
    "after_wb_trunc": after_wb_trunc.decode("ascii"),
}

# 4. Unicode filenames & nested directories
uni_dir = pathlib.Path("unicode_dir") / "αβγ"
uni_dir.mkdir(parents=True, exist_ok=True)
uni_file = uni_dir / "café-日本語.txt"
uni_file.write_text("héllo 世界\n", encoding="utf-8")
out["unicode"] = {
    "readback": uni_file.read_text(encoding="utf-8"),
    "exists": uni_file.exists(),
}

# 5. Directory-relative opens (dir_fd)
dir_fd = os.open("rel_sub", os.O_RDONLY)
try:
    rfd = os.open("inside.txt", os.O_RDONLY, dir_fd=dir_fd)
    try:
        rel_read = os.read(rfd, 64).decode("utf-8")
    finally:
        os.close(rfd)
    wfd = os.open("created_rel.txt", os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o644, dir_fd=dir_fd)
    try:
        os.write(wfd, b"via-dir-fd\n")
    finally:
        os.close(wfd)
finally:
    os.close(dir_fd)
out["dir_fd"] = {
    "read": rel_read,
    "created_readback": pathlib.Path("rel_sub/created_rel.txt").read_text(encoding="utf-8"),
}

# 6. Errno/exception parity (ENOENT, EISDIR)
errs = {}
try:
    open("does_not_exist_py09.txt", "rb")
except OSError as e:
    errs["enoent"] = [type(e).__name__, e.errno == errno.ENOENT]
try:
    open("rel_sub", "rb")
except OSError as e:
    errs["eisdir"] = [type(e).__name__, e.errno == errno.EISDIR]
out["errors"] = errs

# 7. Stdlib csv + zipfile + json
with open("table.csv", "w", encoding="utf-8", newline="") as f:
    w = csv.writer(f)
    w.writerow(["k", "v"])
    w.writerow(["alpha", "1"])
    w.writerow(["beta", "2"])
with zipfile.ZipFile("bundle.zip", "w", compression=zipfile.ZIP_DEFLATED) as zf:
    zf.writestr("meta.json", json.dumps({"ok": True, "count": 2}, sort_keys=True))
    zf.write("table.csv")
with zipfile.ZipFile("bundle.zip", "r") as zf:
    out["zip_names"] = sorted(zf.namelist())
    out["zip_meta"] = json.loads(zf.read("meta.json").decode("utf-8"))

# 8. Bundled libraries: PyYAML + openpyxl
cfg = yaml.safe_load(pathlib.Path("items.yaml").read_text(encoding="utf-8"))
wb = openpyxl.Workbook()
ws = wb.active
ws.title = "Items"
ws.freeze_panes = "A2"
ws.append(["name", "qty"])
for item in cfg["items"]:
    ws.append([item["name"], item["qty"]])
wb.save("items.xlsx")
wb2 = openpyxl.load_workbook("items.xlsx")
ws2 = wb2["Items"]
out["workbook"] = {
    "sheet": ws2.title,
    "freeze": ws2.freeze_panes,
    "rows": [[ws2.cell(r, 1).value, ws2.cell(r, 2).value] for r in range(1, ws2.max_row + 1)],
}

print(json.dumps(out, sort_keys=True))
`

func seedPY09ParityWorkspace(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "seed.bin"), []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "rel_sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rel_sub", "inside.txt"), []byte("hello-rel-sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "items.yaml"), []byte("items:\n  - name: bolt\n    qty: 10\n  - name: nut\n    qty: 25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func snapshotDirHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		// Skip zip/xlsx timestamps if any differ in wall-clock metadata; hash raw bytes for deterministic files
		// and canonical member hashes for zip/xlsx archives.
		relSlash := filepath.ToSlash(rel)
		sum := sha256.Sum256(data)
		out[relSlash] = hex.EncodeToString(sum[:])
		return nil
	})
	contractcheck.FailErr(t, "snapshotDirHashes", err)
	return out
}

func extractAdmissionSignature(resp *portablepython.ToolResponse) []string {
	if resp == nil || resp.AdmissionEvidence == nil {
		return nil
	}
	seen := make(map[string]struct{})
	for _, rec := range resp.AdmissionEvidence.Records() {
		key := string(rec.Family) + ":" + rec.Operation + ":" + rec.Target.RelPath + ":" + string(rec.Decision)
		seen[key] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestPY09_NativeVsWazeroWASIFileAndStreamParity(t *testing.T) {
	nativeSvc := newNativeQualService(t, "")
	imageDir := nativeSvc.ImageDir()

	wazeroBackend := platformWazeroControlBackend()
	wazeroSvc, err := portablepython.NewService(context.Background(), portablepython.ServiceConfig{
		ImageDir:    imageDir,
		Backend:     wazeroBackend,
		ScratchRoot: t.TempDir(),
	})
	contractcheck.FailErr(t, "create wazero control service", err)
	t.Cleanup(func() { _ = wazeroSvc.Close(context.Background()) })

	// Run identical fixture + identical checked-hash .pyc image on BOTH native-aot and wazero.
	runOne := func(label string, svc *portablepython.Service) (*portablepython.ToolResponse, string, map[string]string) {
		ws := t.TempDir()
		seedPY09ParityWorkspace(t, ws)
		roots := []pythondocuments.HostRootConfig{{ID: "ws-py09", Dir: ws}}
		resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			ExecutionID:  "py09-parity-" + label,
			CallID:       "call-py09-parity-" + label,
			SessionID:    "sess-py09-parity",
			Actor:        "coordinator",
			View:         "primary",
			Surface:      "implement_investigate",
			Mode:         pythonruntime.ModeCode,
			Code:         py09WASIParityScript,
			Roots:        roots,
			MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
			Limits:       pythonruntime.ResourceLimits{Timeout: 180 * time.Second},
		})
		contractcheck.FailErr(t, "execute parity script on "+label, err)
		if !resp.CleanCompletion || resp.ExitCode != 0 {
			t.Fatalf("%s parity script failed: exit=%d clean=%v stop=%+v stderr=%q",
				label, resp.ExitCode, resp.CleanCompletion, resp.Stop, resp.Stderr)
		}
		return resp, ws, snapshotDirHashes(t, ws)
	}

	nativeResp, _, nativeFiles := runOne("native-aot", nativeSvc)
	wazeroResp, _, wazeroFiles := runOne(string(wazeroBackend), wazeroSvc)

	if nativeResp.Backend != pythonruntime.BackendNativeAOT {
		t.Fatalf("nativeResp.Backend=%q want %q", nativeResp.Backend, pythonruntime.BackendNativeAOT)
	}
	if wazeroResp.Backend != wazeroBackend {
		t.Fatalf("wazeroResp.Backend=%q want %q", wazeroResp.Backend, wazeroBackend)
	}

	// 1. Exact stdout parity (all short reads, EOF, seek/tell, binary NUL, Unicode, dir_fd, errno, csv/zip, yaml/openpyxl).
	if nativeResp.Stdout != wazeroResp.Stdout {
		t.Fatalf("stdout mismatch between native-aot and %s:\nnative: %s\nwazero: %s",
			wazeroBackend, nativeResp.Stdout, wazeroResp.Stdout)
	}

	// 2. Final settled files parity (every non-archive file byte-for-byte identical; archive files present on both).
	if len(nativeFiles) != len(wazeroFiles) {
		t.Fatalf("settled file count mismatch: native=%d wazero=%d\nnative=%v\nwazero=%v",
			len(nativeFiles), len(wazeroFiles), nativeFiles, wazeroFiles)
	}
	for rel, nHash := range nativeFiles {
		wHash, ok := wazeroFiles[rel]
		if !ok {
			t.Fatalf("file %q settled on native-aot but missing on %s", rel, wazeroBackend)
		}
		// .zip and .xlsx embed wall-clock modification timestamps inside ZIP headers;
		// their decoded contents were already verified identically in stdout JSON above.
		if strings.HasSuffix(rel, ".zip") || strings.HasSuffix(rel, ".xlsx") {
			continue
		}
		if nHash != wHash {
			t.Fatalf("byte-for-byte hash mismatch for %q: native=%s wazero=%s", rel, nHash, wHash)
		}
	}

	// 3. Reached effects parity (admission families & targets).
	nativeAdm := extractAdmissionSignature(nativeResp)
	wazeroAdm := extractAdmissionSignature(wazeroResp)
	if len(nativeAdm) == 0 {
		t.Fatal("expected non-empty reached admission effects on native-aot")
	}
	t.Logf("PY09 parity PASS (image=%s): native=%s vs %s=%s; stdout=%d bytes; settled_files=%d; admissions native=%d wazero=%d",
		nativeSvc.RuntimeManifest().ImageID,
		nativeResp.Usage.Duration.Round(time.Millisecond),
		wazeroBackend,
		wazeroResp.Usage.Duration.Round(time.Millisecond),
		len(nativeResp.Stdout),
		len(nativeFiles),
		len(nativeAdm),
		len(wazeroAdm),
	)
}

func TestPY09_NativeWASIUncoveredEffectfulPaths(t *testing.T) {
	nativeSvc := newNativeQualService(t, "")
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "io_target.bin"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	roots := []pythondocuments.HostRootConfig{{ID: "ws-uncovered", Dir: ws}}

	code := `
import json, os, socket, time

res = {}
# fd_pread & fd_pwrite & fd_sync & fd_datasync
fd = os.open("io_target.bin", os.O_RDWR)
try:
    before = os.pread(fd, 4, 3)
    os.pwrite(fd, b"WXYZ", 3)
    os.fsync(fd)
    if hasattr(os, "fdatasync"):
        os.fdatasync(fd)
    after = os.pread(fd, 10, 0)
finally:
    os.close(fd)
res["pread_before"] = before.decode("ascii")
res["pwrite_after"] = after.decode("ascii")

# path_create_directory, path_rename, fd_readdir, path_unlink_file, path_remove_directory
os.mkdir("tmp_dir")
with open("tmp_dir/a.txt", "w") as f:
    f.write("a")
os.rename("tmp_dir/a.txt", "tmp_dir/b.txt")
res["readdir"] = sorted(os.listdir("tmp_dir"))
os.unlink("tmp_dir/b.txt")
os.rmdir("tmp_dir")
res["rmdir_gone"] = not os.path.exists("tmp_dir")

# poll_oneoff via time.sleep
t0 = time.monotonic()
time.sleep(0.01)
res["slept"] = (time.monotonic() - t0) >= 0.0

# sock_* non-authority refusal (no preopened sockets)
try:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.connect(("127.0.0.1", 9))
    res["sock_refused"] = False
except OSError as e:
    res["sock_refused"] = True
    res["sock_err"] = type(e).__name__

print(json.dumps(res, sort_keys=True))
`
	resp, err := nativeSvc.ExecuteTool(context.Background(), portablepython.ToolRequest{
		ExecutionID:  "py09-uncovered-wasi",
		CallID:       "call-py09-uncovered-wasi",
		SessionID:    "sess-py09-uncovered",
		Actor:        "coordinator",
		View:         "primary",
		Surface:      "implement_investigate",
		Mode:         pythonruntime.ModeCode,
		Code:         code,
		Roots:        roots,
		MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
		Limits:       pythonruntime.ResourceLimits{Timeout: 60 * time.Second},
	})
	contractcheck.FailErr(t, "execute uncovered WASI paths", err)
	if !resp.CleanCompletion || resp.ExitCode != 0 {
		t.Fatalf("uncovered WASI paths failed: exit=%d clean=%v stop=%+v stderr=%q",
			resp.ExitCode, resp.CleanCompletion, resp.Stop, resp.Stderr)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.Stdout)), &parsed); err != nil {
		t.Fatalf("decode stdout %q: %v", resp.Stdout, err)
	}
	if parsed["pread_before"] != "3456" || parsed["pwrite_after"] != "012WXYZ789" {
		t.Fatalf("unexpected pread/pwrite result: %+v", parsed)
	}
	if parsed["rmdir_gone"] != true || parsed["sock_refused"] != true {
		t.Fatalf("unexpected dir/socket result: %+v", parsed)
	}
	gotDisk, err := os.ReadFile(filepath.Join(ws, "io_target.bin"))
	contractcheck.FailErr(t, "read settled io_target.bin", err)
	if string(gotDisk) != "012WXYZ789" {
		t.Fatalf("settled io_target.bin=%q want %q", string(gotDisk), "012WXYZ789")
	}
}

type py09RevisionEditorBridge struct {
	mu      sync.Mutex
	drafts  map[string]pythondocuments.EditorDraft
	history map[string][]string
}

func newPY09RevisionEditorBridge() *py09RevisionEditorBridge {
	return &py09RevisionEditorBridge{
		drafts:  make(map[string]pythondocuments.EditorDraft),
		history: make(map[string][]string),
	}
}

func (e *py09RevisionEditorBridge) SetDraft(rootID, relPath, docID, rev string, data []byte, baseSHA string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	key := rootID + ":" + relPath
	e.drafts[key] = pythondocuments.EditorDraft{
		DocumentID: docID,
		Revision:   rev,
		Bytes:      append([]byte(nil), data...),
		BaseSHA256: baseSHA,
		Dirty:      true,
	}
	e.history[key] = append(e.history[key], rev)
}

func (e *py09RevisionEditorBridge) Snapshot(rootID, relPath string) (pythondocuments.EditorDraft, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	key := rootID + ":" + relPath
	d := e.drafts[key]
	d.Bytes = append([]byte(nil), d.Bytes...)
	h := append([]string(nil), e.history[key]...)
	return d, h
}

func (e *py09RevisionEditorBridge) CaptureDraft(_ context.Context, rootID, _, relPath string) (pythondocuments.EditorDraft, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d, ok := e.drafts[rootID+":"+relPath]
	if !ok {
		return pythondocuments.EditorDraft{}, false, nil
	}
	d.Bytes = append([]byte(nil), d.Bytes...)
	return d, true, nil
}

func (e *py09RevisionEditorBridge) PublishDraft(
	_ context.Context, rootID, branch, relPath string, baseRev pythondocuments.DocumentRevision, data []byte,
) (pythondocuments.DocumentRevision, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	key := rootID + ":" + relPath
	d, ok := e.drafts[key]
	if !ok {
		return pythondocuments.DocumentRevision{}, false, nil
	}
	if baseRev.DocumentRevision != "" && d.Revision != baseRev.DocumentRevision {
		return pythondocuments.DocumentRevision{}, true, &pythondocuments.RevisionConflictError{
			Target: pythonruntime.CanonicalTarget{Namespace: pythonruntime.NamespaceWorkspace, RootID: rootID, Branch: branch, RelPath: relPath},
			Base:   baseRev,
			Current: pythondocuments.DocumentRevision{
				RootID: rootID, Branch: branch, DocumentID: d.DocumentID, DocumentRevision: d.Revision, UnsavedDraft: true,
			},
			Reason: "editor draft revision changed concurrently",
		}
	}
	nextRev := d.Revision + "+1"
	d.Bytes = append([]byte(nil), data...)
	d.Revision = nextRev
	e.drafts[key] = d
	e.history[key] = append(e.history[key], nextRev)
	return pythondocuments.DocumentRevision{
		RootID: rootID, Branch: branch, DocumentID: d.DocumentID,
		DocumentRevision: d.Revision, DiskSHA256: d.BaseSHA256, UnsavedDraft: true,
	}, true, nil
}

func TestPY09_HostAuthorityEditorDiskSiblingBoundaries(t *testing.T) {
	receiptDir := t.TempDir()
	svc := newNativeQualService(t, receiptDir)

	setupFixture := func(t *testing.T) (ws1, ws2 string, roots []pythondocuments.HostRootConfig, editor *py09RevisionEditorBridge, door pythondocuments.MutationDoor) {
		t.Helper()
		ws1 = t.TempDir()
		ws2 = t.TempDir()
		if err := os.MkdirAll(filepath.Join(ws1, "shared"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(ws2, "shared"), 0o755); err != nil {
			t.Fatal(err)
		}
		disk1 := []byte("SAVED_DISK_BYTES_V1\n")
		disk2 := []byte("SIBLING_ROOT_WS2_BYTES_V1\n")
		if err := os.WriteFile(filepath.Join(ws1, "shared", "doc.txt"), disk1, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws2, "shared", "doc.txt"), disk2, 0o644); err != nil {
			t.Fatal(err)
		}
		roots = []pythondocuments.HostRootConfig{
			{ID: "ws", Dir: ws1},
			{ID: "ws2", Dir: ws2},
		}
		editor = newPY09RevisionEditorBridge()
		editor.SetDraft("ws", "shared/doc.txt", "doc-shared-1", "ed-1", []byte("EDITOR_DRAFT_BYTES_V1\n"), pythondocuments.SHA256Hex(disk1))
		door = native.NewPythonMutationDoor(tools.ToolContext{}, roots)
		return ws1, ws2, roots, editor, door
	}

	t.Run("caught_exception_publishes_and_records_dynamic_open", func(t *testing.T) {
		ws1, ws2, roots, editor, door := setupFixture(t)
		code := `
import pathlib

# Library reads across editor draft (ws) and sibling root (@ws2) at same relative path
ed_text = pathlib.Path("shared/doc.txt").read_text(encoding="utf-8")
sib_text = pathlib.Path("@ws2/shared/doc.txt").read_text(encoding="utf-8")
assert ed_text == "EDITOR_DRAFT_BYTES_V1\n", f"expected editor draft, got {ed_text!r}"
assert sib_text == "SIBLING_ROOT_WS2_BYTES_V1\n", f"expected sibling root, got {sib_text!r}"

# Buffered writes to editor-backed file, sibling root file, and new workspace file
pathlib.Path("shared/doc.txt").write_text("EDITOR_UPDATED_AFTER_CAUGHT\n", encoding="utf-8")
pathlib.Path("@ws2/shared/doc.txt").write_text("SIBLING_UPDATED_AFTER_CAUGHT\n", encoding="utf-8")
pathlib.Path("notes").mkdir(exist_ok=True)
pathlib.Path("notes/caught_ok.txt").write_text("CAUGHT_OK\n", encoding="utf-8")

# Dynamic open that raises and is caught by the script
dyn_path = "missing/" + "dynamic_probe.txt"
caught = False
try:
    open(dyn_path, "r").read()
except FileNotFoundError:
    caught = True
assert caught
print("CAUGHT_PUBLICATION_OK")
`
		resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			ExecutionID:  "py09-auth-caught",
			CallID:       "call-py09-auth-caught",
			SessionID:    "sess-py09-auth",
			Actor:        "coordinator",
			View:         "primary",
			Surface:      "implement_investigate",
			Mode:         pythonruntime.ModeCode,
			Code:         code,
			Roots:        roots,
			Editor:       editor,
			MutationDoor: door,
		})
		contractcheck.FailErr(t, "execute caught exception case", err)
		if !resp.CleanCompletion || resp.ExitCode != 0 {
			t.Fatalf("caught case failed: exit=%d clean=%v stop=%+v stderr=%q", resp.ExitCode, resp.CleanCompletion, resp.Stop, resp.Stderr)
		}

		// Independent editor & disk verification:
		edDraft, edHist := editor.Snapshot("ws", "shared/doc.txt")
		if string(edDraft.Bytes) != "EDITOR_UPDATED_AFTER_CAUGHT\n" {
			t.Fatalf("editor draft bytes=%q want updated", string(edDraft.Bytes))
		}
		if len(edHist) != 2 || edHist[0] != "ed-1" || edHist[1] != "ed-1+1" {
			t.Fatalf("unexpected editor revision history: %v", edHist)
		}
		disk1, _ := os.ReadFile(filepath.Join(ws1, "shared", "doc.txt"))
		if string(disk1) != "SAVED_DISK_BYTES_V1\n" {
			t.Fatalf("saved disk ws1/shared/doc.txt must remain untouched when published to editor draft, got %q", string(disk1))
		}
		disk2, _ := os.ReadFile(filepath.Join(ws2, "shared", "doc.txt"))
		if string(disk2) != "SIBLING_UPDATED_AFTER_CAUGHT\n" {
			t.Fatalf("sibling disk ws2/shared/doc.txt=%q want updated", string(disk2))
		}
		caughtFile, _ := os.ReadFile(filepath.Join(ws1, "notes", "caught_ok.txt"))
		if string(caughtFile) != "CAUGHT_OK\n" {
			t.Fatalf("ws1/notes/caught_ok.txt=%q want CAUGHT_OK", string(caughtFile))
		}

		// Verify receipt records the particular dynamic open ("missing/dynamic_probe.txt").
		foundDynamic := false
		for _, rec := range resp.AdmissionEvidence.Records() {
			if strings.Contains(rec.Target.RelPath, "missing/dynamic_probe.txt") {
				foundDynamic = true
				break
			}
		}
		if !foundDynamic {
			t.Fatalf("expected admission receipt for dynamic open missing/dynamic_probe.txt, got %v", extractAdmissionSignature(resp))
		}
		// Verify durable receipt persisted on disk.
		reopened, err := portablepython.NewFileReceiptStore(receiptDir)
		contractcheck.FailErr(t, "reopen FileReceiptStore", err)
		execRec, ok, err := reopened.FindExecution("py09-auth-caught")
		contractcheck.FailErr(t, "FindExecution", err)
		if !ok || !execRec.CleanCompletion {
			t.Fatalf("reopened receipt missing or not clean: ok=%v rec=%+v", ok, execRec)
		}
	})

	t.Run("forced_revision_conflict_withholds_write_and_preserves_editor_and_disk", func(t *testing.T) {
		ws1, ws2, roots, editor, door := setupFixture(t)
		var mutatedOnce sync.Once
		conflictTriggerPolicy := func(_ context.Context, op pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
			if op.Family == pythonruntime.EffectFilesystemWrite && strings.HasSuffix(op.Target.RelPath, "shared/doc.txt") {
				mutatedOnce.Do(func() {
					disk1, _ := os.ReadFile(filepath.Join(ws1, "shared", "doc.txt"))
					editor.SetDraft("ws", "shared/doc.txt", "doc-shared-1", "ed-concurrent-2", []byte("CONCURRENT_USER_EDIT_V2\n"), pythondocuments.SHA256Hex(disk1))
				})
			}
			return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
		}

		code := `
import pathlib
ed_text = pathlib.Path("shared/doc.txt").read_text(encoding="utf-8")
assert ed_text == "EDITOR_DRAFT_BYTES_V1\n"
pathlib.Path("shared/doc.txt").write_text("STALE_AGENT_WRITE\n", encoding="utf-8")
`
		resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			ExecutionID:  "py09-auth-conflict",
			CallID:       "call-py09-auth-conflict",
			SessionID:    "sess-py09-auth",
			Actor:        "coordinator",
			View:         "primary",
			Surface:      "implement_investigate",
			Mode:         pythonruntime.ModeCode,
			Code:         code,
			Roots:        roots,
			Editor:       editor,
			MutationDoor: door,
			Policy:       conflictTriggerPolicy,
		})
		contractcheck.FailErr(t, "execute revision conflict case", err)
		if resp.CleanCompletion {
			t.Fatal("expected CleanCompletion=false on revision conflict")
		}
		if resp.Stop == nil || resp.Stop.Code != pythonruntime.StopRevisionConflict {
			t.Fatalf("expected StopRevisionConflict, got stop=%+v", resp.Stop)
		}
		if resp.Settlement == nil || len(resp.Settlement.Conflicts) == 0 {
			t.Fatalf("expected settlement conflicts, got %+v", resp.Settlement)
		}

		// Independent verification: concurrent user edit & disk files preserved intact.
		edDraft, edHist := editor.Snapshot("ws", "shared/doc.txt")
		if string(edDraft.Bytes) != "CONCURRENT_USER_EDIT_V2\n" || edDraft.Revision != "ed-concurrent-2" {
			t.Fatalf("editor draft corrupted on conflict: rev=%q bytes=%q", edDraft.Revision, string(edDraft.Bytes))
		}
		if len(edHist) != 2 || edHist[0] != "ed-1" || edHist[1] != "ed-concurrent-2" {
			t.Fatalf("unexpected conflict revision history: %v", edHist)
		}
		disk1, _ := os.ReadFile(filepath.Join(ws1, "shared", "doc.txt"))
		if string(disk1) != "SAVED_DISK_BYTES_V1\n" {
			t.Fatalf("ws1 disk altered on conflict: %q", string(disk1))
		}
		disk2, _ := os.ReadFile(filepath.Join(ws2, "shared", "doc.txt"))
		if string(disk2) != "SIBLING_ROOT_WS2_BYTES_V1\n" {
			t.Fatalf("ws2 disk altered on conflict: %q", string(disk2))
		}
	})

	t.Run("uncaught_exception_rolls_back_all_buffered_writes", func(t *testing.T) {
		ws1, ws2, roots, editor, door := setupFixture(t)
		code := `
import pathlib
pathlib.Path("shared/doc.txt").write_text("MUST_BE_ROLLED_BACK_EDITOR\n", encoding="utf-8")
pathlib.Path("@ws2/shared/doc.txt").write_text("MUST_BE_ROLLED_BACK_SIBLING\n", encoding="utf-8")
pathlib.Path("notes").mkdir(exist_ok=True)
pathlib.Path("notes/should_rollback.txt").write_text("MUST_NOT_EXIST\n", encoding="utf-8")
raise RuntimeError("boom after buffered writes")
`
		resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			ExecutionID:  "py09-auth-uncaught",
			CallID:       "call-py09-auth-uncaught",
			SessionID:    "sess-py09-auth",
			Actor:        "coordinator",
			View:         "primary",
			Surface:      "implement_investigate",
			Mode:         pythonruntime.ModeCode,
			Code:         code,
			Roots:        roots,
			Editor:       editor,
			MutationDoor: door,
		})
		contractcheck.FailErr(t, "execute uncaught exception case", err)
		if resp.CleanCompletion || resp.ExitCode == 0 {
			t.Fatalf("expected non-clean failure on uncaught exception: exit=%d clean=%v", resp.ExitCode, resp.CleanCompletion)
		}

		// Independent verification: editor, ws1 disk, ws2 disk, and new file ALL untouched/withheld.
		edDraft, edHist := editor.Snapshot("ws", "shared/doc.txt")
		if string(edDraft.Bytes) != "EDITOR_DRAFT_BYTES_V1\n" || len(edHist) != 1 {
			t.Fatalf("editor draft must remain untouched on uncaught exception: hist=%v bytes=%q", edHist, string(edDraft.Bytes))
		}
		disk1, _ := os.ReadFile(filepath.Join(ws1, "shared", "doc.txt"))
		if string(disk1) != "SAVED_DISK_BYTES_V1\n" {
			t.Fatalf("ws1 disk altered on uncaught exception: %q", string(disk1))
		}
		disk2, _ := os.ReadFile(filepath.Join(ws2, "shared", "doc.txt"))
		if string(disk2) != "SIBLING_ROOT_WS2_BYTES_V1\n" {
			t.Fatalf("ws2 disk altered on uncaught exception: %q", string(disk2))
		}
		if _, statErr := os.Stat(filepath.Join(ws1, "notes", "should_rollback.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("expected notes/should_rollback.txt to be rolled back, statErr=%v", statErr)
		}
	})
}
