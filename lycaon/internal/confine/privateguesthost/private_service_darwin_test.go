package privateguesthost

import (
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestPrivateGuestAuthenticatesRunningImage(t *testing.T) {
	const path = "/usr/bin/true"
	output, err := exec.Command("/usr/bin/codesign", "-d", "--verbose=4", path).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect system fixture: %v: %s", err, output)
	}
	var cdhash string
	for line := range strings.Lines(string(output)) {
		if value, ok := strings.CutPrefix(line, "CDHash="); ok {
			cdhash = strings.TrimSpace(value)
			break
		}
	}
	if raw, err := hex.DecodeString(cdhash); err != nil || len(raw) != 20 {
		t.Fatalf("invalid fixture identity %q", cdhash)
	}
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	fds := []*os.File{file, file, file, file}
	if _, err := spawnGuest(path, strings.Repeat("0", 40), fds); err == nil {
		t.Fatal("resumed an unrecognized image")
	}
	process, err := spawnGuest(path, cdhash, fds)
	if err != nil {
		t.Fatal(err)
	}
	state, err := process.Wait()
	if err != nil || !state.Success() {
		t.Fatalf("verified image: %v, %v", state, err)
	}
}
