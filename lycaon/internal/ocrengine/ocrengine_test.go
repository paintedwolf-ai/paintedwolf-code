package ocrengine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadPinDescribesEveryShippedPlatform(t *testing.T) {
	pin, err := LoadPin()
	if err != nil {
		testutil.FailErr(t, "load pin", err)
	}
	for _, key := range []string{"linux-amd64", "linux-arm64"} {
		plat, ok := pin.Platforms[key]
		if !ok || plat.URL == "" {
			t.Fatalf("pin lacks %s: %+v", key, plat)
		}
	}
	prefix := "https://github.com/" + pin.Producer.Repository + "/releases/download/v{engine_version}/"
	for key, plat := range pin.Platforms {
		if !strings.HasPrefix(plat.URL, prefix) {
			t.Fatalf("%s archive %q is not the producer release %s", key, plat.URL, pin.Producer.Tag)
		}
	}
	if _, ok := pin.Platform("darwin", "arm64"); ok {
		t.Fatal("macOS recognizes text in-process and must not pin an engine")
	}
	for _, p := range pin.Payloads(PlatformPin{BinarySHA256: "ab", BinaryBytes: 1}) {
		if p.Rel == "" || (p.Rel != binaryRel && (!validDigest(p.SHA256) || p.Bytes <= 0)) {
			t.Fatalf("payload %+v is not pinned", p)
		}
	}
}

func TestPinValidationRequiresProducerTagAndTrainedData(t *testing.T) {
	data := pinPayload{SHA256: strings.Repeat("a", 64), Bytes: 1}
	valid := Pin{
		EngineVersion: "5.5.3+paintedwolf.1",
		Producer:      pinProducer{Repository: "paintedwolf-ai/paintedwolf-tesseract", Tag: "v5.5.3+paintedwolf.1", Commit: strings.Repeat("b", 40)},
		TrainedData:   map[string]pinPayload{"eng_traineddata": data, "osd_traineddata": data},
	}
	if err := valid.validate(); err != nil {
		testutil.FailErr(t, "valid pin", err)
	}
	otherTag := valid
	otherTag.Producer.Tag = "v5.5.3+paintedwolf.2"
	noCommit := valid
	noCommit.Producer.Commit = "2636845"
	upperCommit := valid
	upperCommit.Producer.Commit = strings.Repeat("B", 40)
	noData := valid
	noData.TrainedData = map[string]pinPayload{"eng_traineddata": data}
	for name, pin := range map[string]Pin{"tag": otherTag, "commit": noCommit, "uppercase commit": upperCommit, "trained data": noData, "version": {}} {
		if err := pin.validate(); err == nil {
			t.Fatalf("%s: invalid pin accepted", name)
		}
	}
}

// writePayload writes content under root and returns its pinned identity.
func writePayload(t *testing.T, root, rel string, content []byte, mode os.FileMode) Payload {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		testutil.FailErr(t, "write payload", err)
	}
	sum := sha256.Sum256(content)
	return Payload{Rel: rel, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(content))}
}

// stagedPin builds a root whose payloads match a pin built from them.
func stagedPin(t *testing.T) (string, *Pin, PlatformPin) {
	t.Helper()
	root := t.TempDir()
	bin := writePayload(t, root, binaryRel, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	eng := writePayload(t, root, englishTrainedDataRel, []byte("eng"), 0o644)
	osd := writePayload(t, root, osdTrainedDataRel, []byte("osd"), 0o644)
	pin := &Pin{
		EngineVersion: "test",
		TrainedData: map[string]pinPayload{
			"eng_traineddata": {SHA256: eng.SHA256, Bytes: eng.Bytes},
			"osd_traineddata": {SHA256: osd.SHA256, Bytes: osd.Bytes},
		},
	}
	return root, pin, PlatformPin{BinarySHA256: bin.SHA256, BinaryBytes: bin.Bytes}
}

func TestVerifyRootAcceptsPinnedPayload(t *testing.T) {
	resetForTest()
	root, pin, plat := stagedPin(t)
	engine, err := verifyRoot(root, pin, plat)
	if err != nil {
		testutil.FailErr(t, "verify", err)
	}
	if engine.Binary != filepath.Join(root, "bin", "tesseract") || engine.TessdataDir != filepath.Join(root, "tessdata") || engine.Version != "test" {
		t.Fatalf("engine = %+v", engine)
	}
	// A verified root is remembered; a changed payload is not re-read.
	if err := os.WriteFile(engine.Binary, []byte("changed"), 0o755); err != nil {
		testutil.FailErr(t, "rewrite", err)
	}
	if _, err := verifyRoot(root, pin, plat); err != nil {
		t.Fatalf("remembered root re-verified: %v", err)
	}
}

func TestVerifyRootRefusesMismatchedAndMissingPayload(t *testing.T) {
	resetForTest()
	root, pin, plat := stagedPin(t)
	osd := filepath.Join(root, "tessdata", "osd.traineddata")
	if err := os.WriteFile(osd, []byte("OSD"), 0o644); err != nil {
		testutil.FailErr(t, "tamper", err)
	}
	_, err := verifyRoot(root, pin, plat)
	var ue *UnavailableError
	if !errors.As(err, &ue) || ue.Reason != ReasonDigestMismatch || ue.Path != osd {
		t.Fatalf("tampered payload err = %v", err)
	}
	if err := os.WriteFile(osd, []byte("osd!"), 0o644); err != nil {
		testutil.FailErr(t, "resize", err)
	}
	if _, err := verifyRoot(root, pin, plat); !errors.As(err, &ue) || ue.Reason != ReasonDigestMismatch {
		t.Fatalf("resized payload err = %v", err)
	}
	if err := os.Remove(osd); err != nil {
		testutil.FailErr(t, "remove", err)
	}
	if _, err := verifyRoot(root, pin, plat); !errors.As(err, &ue) || ue.Reason != ReasonMissing {
		t.Fatalf("missing payload err = %v", err)
	}
}

func TestVerifyRootRequiresExecutableBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not meaningful on Windows")
	}
	resetForTest()
	root, pin, plat := stagedPin(t)
	if err := os.Chmod(filepath.Join(root, "bin", "tesseract"), 0o644); err != nil {
		testutil.FailErr(t, "chmod", err)
	}
	_, err := verifyRoot(root, pin, plat)
	var ue *UnavailableError
	if !errors.As(err, &ue) || ue.Reason != ReasonMissing {
		t.Fatalf("non-executable binary err = %v", err)
	}
}

func TestResolveWithoutEngineReportsPlatformOrMissing(t *testing.T) {
	resetForTest()
	t.Setenv(EnvRoot, t.TempDir())
	t.Setenv("LYCAON_ENGINE_ROOT", t.TempDir())
	_, err := Resolve()
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v", err)
	}
	want := ReasonMissing
	pin, pinErr := LoadPin()
	if pinErr != nil {
		testutil.FailErr(t, "load pin", pinErr)
	}
	if _, ok := pin.Platform(runtime.GOOS, runtime.GOARCH); !ok {
		want = ReasonUnsupportedPlatform
	}
	if ue.Reason != want {
		t.Fatalf("reason = %q, want %q (%v)", ue.Reason, want, err)
	}
}

// platformPin is a pin that ships the staged payload for linux/amd64 only.
func platformPin(pin *Pin, plat PlatformPin) *Pin {
	withPlatform := *pin
	withPlatform.Platforms = map[string]PlatformPin{"linux-amd64": plat}
	return &withPlatform
}

func TestResolveRefusesAPlatformWithoutAnEngine(t *testing.T) {
	resetForTest()
	root, pin, plat := stagedPin(t)
	_, err := resolve(platformPin(pin, plat), "darwin", "arm64", []string{root})
	var ue *UnavailableError
	if !errors.As(err, &ue) || ue.Reason != ReasonUnsupportedPlatform || !strings.Contains(err.Error(), "no bundled text recognizer") {
		t.Fatalf("unsupported platform err = %v", err)
	}
}

func TestResolveSkipsMissingRootsAndUsesTheFirstVerifiedOne(t *testing.T) {
	resetForTest()
	root, pin, plat := stagedPin(t)
	engine, err := resolve(platformPin(pin, plat), "linux", "amd64", []string{t.TempDir(), root})
	if err != nil {
		testutil.FailErr(t, "resolve", err)
	}
	if engine.Root != filepath.Clean(root) {
		t.Fatalf("resolved %q, want %q", engine.Root, root)
	}
}

// An altered payload in an earlier root is refused even when a later root
// would verify, so a tampered install never falls back to another copy.
func TestResolveStopsAtAnAlteredRoot(t *testing.T) {
	resetForTest()
	good, pin, plat := stagedPin(t)
	altered, _, _ := stagedPin(t)
	osd := filepath.Join(altered, "tessdata", "osd.traineddata")
	if err := os.WriteFile(osd, []byte("OSD"), 0o644); err != nil {
		testutil.FailErr(t, "tamper", err)
	}
	_, err := resolve(platformPin(pin, plat), "linux", "amd64", []string{altered, good})
	var ue *UnavailableError
	if !errors.As(err, &ue) || ue.Reason != ReasonDigestMismatch || ue.Path != osd {
		t.Fatalf("altered root err = %v", err)
	}
	if !strings.Contains(err.Error(), "differs from the pinned payload") {
		t.Fatalf("altered root message = %q", err)
	}
}

func TestResolveWithNoRootIsMissing(t *testing.T) {
	resetForTest()
	_, pin, plat := stagedPin(t)
	_, err := resolve(platformPin(pin, plat), "linux", "amd64", nil)
	var ue *UnavailableError
	if !errors.As(err, &ue) || ue.Reason != ReasonMissing || err.Error() != "ocrengine: bundled text recognizer unavailable" {
		t.Fatalf("no root err = %v", err)
	}
}

func TestParsePinRejectsMalformedAndIncompletePins(t *testing.T) {
	if _, err := parsePin([]byte("engine_version: [")); err == nil || !strings.Contains(err.Error(), "parse pin") {
		t.Fatalf("malformed pin err = %v", err)
	}
	if _, err := parsePin([]byte("engine_version: \"5.5.3+paintedwolf.1\"\n")); err == nil || !strings.Contains(err.Error(), "producer") {
		t.Fatalf("incomplete pin err = %v", err)
	}
}

func TestTestingEnableBundledRootFindsStagedTree(t *testing.T) {
	resetForTest()
	t.Setenv(EnvRoot, "")
	checkout := t.TempDir()
	if err := os.WriteFile(filepath.Join(checkout, "Taskfile.yml"), nil, 0o644); err != nil {
		testutil.FailErr(t, "taskfile", err)
	}
	staged := filepath.Join(checkout, "lycaon-den", "src-tauri", "engine-root", DirName)
	writePayload(t, staged, binaryRel, []byte("bin"), 0o755)
	nested := filepath.Join(checkout, "lycaon", "internal", "visualscreen")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	t.Chdir(nested)
	TestingEnableBundledRoot()
	if got := os.Getenv(EnvRoot); got != staged {
		t.Fatalf("%s = %q, want %q", EnvRoot, got, staged)
	}
}
