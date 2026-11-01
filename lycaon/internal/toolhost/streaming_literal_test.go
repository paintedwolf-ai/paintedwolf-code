package toolhost

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandLiteralTransformsStreamLargeInputs(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	memory := observedProgramMemory(t, env)
	chunk := make([]byte, 1_000_000)
	for i := range chunk {
		chunk[i] = byte(i)
	}
	sum := writeRepeatedFixture(t, filepath.Join(env.Dir, "large.bin"), chunk, 50)
	out, err := env.Call(t, t.Context(), `base64 -w 0 large.bin | base64 -d > decoded; sha256sum decoded`)
	testutil.FailErr(t, "env.Call failed", err)
	result := decodePipeOutcome(t, out)
	if result.State != "completed" || result.Status != 0 || result.Stdout != sum+"  decoded\n" {
		t.Fatalf("streamed base64 %+v refusal %+v", result, result.Refusal)
	}
	file, err := os.Open(filepath.Join(env.Dir, "decoded"))
	testutil.FailErr(t, "os.Open failed", err)
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	_ = file.Close()
	if err != nil || n != 50_000_000 || fmt.Sprintf("%x", hash.Sum(nil)) != sum {
		t.Fatalf("binary roundtrip bytes %d err %v", n, err)
	}
	if memory().Peak().State > 4<<20 {
		t.Fatalf("base64 gathered input %+v", memory().Peak())
	}
	out, err = env.Call(t, t.Context(), `xxd -p -s -16 large.bin`)
	testutil.FailErr(t, "env.Call failed", err)
	result = decodePipeOutcome(t, out)
	if result.State != "completed" || result.Status != 0 || result.Stdout != hex.EncodeToString(chunk[len(chunk)-16:])+"\n" {
		t.Fatalf("pinned negative offset %+v", result)
	}
	if memory().Peak().State > 4<<20 {
		t.Fatalf("xxd gathered input %+v", memory().Peak())
	}
	out, err = env.Call(t, t.Context(), `hexdump -C -n 32 large.bin`)
	testutil.FailErr(t, "env.Call failed", err)
	result = decodePipeOutcome(t, out)
	if result.State != "completed" || result.Status != 0 || !strings.HasSuffix(result.Stdout, "00000020\n") {
		t.Fatalf("bounded hexdump %+v", result)
	}
	if memory().Peak().State > 4<<20 {
		t.Fatalf("hexdump gathered input %+v", memory().Peak())
	}
}

func TestCommandChecksumManifestStreamsBeyondWholeInputLimit(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	memory := observedProgramMemory(t, env)
	// Each line is below record_bytes. Invalid manifest syntax is an ordinary
	// utility error; reading all 36 MiB must not become a whole-input refusal.
	chunk := append(bytes.Repeat([]byte{'x'}, (1<<20)-1), '\n')
	writeRepeatedFixture(t, filepath.Join(env.Dir, "manifest"), chunk, 36)
	out, err := env.Call(t, t.Context(), `sha256sum -c manifest`)
	testutil.FailErr(t, "env.Call failed", err)
	result := decodePipeOutcome(t, out)
	if result.State != "completed" || result.Status != 2 || result.Refusal != nil || strings.Count(result.Stderr, "improperly formatted checksum line") != 36 {
		t.Fatalf("manifest stream %+v", result)
	}
	if memory().Peak().State > 4<<20 {
		t.Fatalf("manifest gathered input %+v", memory().Peak())
	}
	if held := memory().InUse(); held.Working != 0 || held.State > 64<<10 {
		t.Fatalf("manifest leases remain %+v", held)
	}
}
