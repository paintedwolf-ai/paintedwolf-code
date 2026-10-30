package nativegen

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"
)

// The end-to-end check drives the compiled nativegen binary over stdio, so it
// needs the release binary; build it from the engine crate before running.
func TestServiceRoundTripOverStdio(t *testing.T) {
	bin := os.Getenv("NATIVEGEN_BIN")
	if bin == "" {
		t.Skip("NATIVEGEN_BIN not set; build the nativegen release binary to run this check")
	}
	dir := os.Getenv("NATIVEGEN_MODEL")
	if dir == "" {
		t.Skip("NATIVEGEN_MODEL not set; point it at a checkpoint directory")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("nativegen binary: %v", err)
	}

	cmd := exec.Command(bin, "--model", dir, "--device", "cpu")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		stdin.Close()
		_ = cmd.Wait()
	}()

	r := bufio.NewReader(stdout)
	var mu sync.Mutex

	send := func(v map[string]any) error {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		var head [4]byte
		binary.LittleEndian.PutUint32(head[:], uint32(len(raw)))
		if _, err := stdin.Write(head[:]); err != nil {
			return err
		}
		_, err = stdin.Write(raw)
		return err
	}
	recv := func() (map[string]any, error) {
		var head [4]byte
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return nil, err
		}
		n := binary.LittleEndian.Uint32(head[:])
		raw := make([]byte, n)
		if _, err := io.ReadFull(r, raw); err != nil {
			return nil, err
		}
		var out map[string]any
		return out, json.Unmarshal(raw, &out)
	}

	if err := send(map[string]any{"method": "hello"}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	reply, err := recv()
	if err != nil {
		t.Fatalf("hello reply: %v", err)
	}
	if reply["service"] != "nativegen" {
		t.Fatalf("hello reply: %v", reply)
	}

	if err := send(map[string]any{"method": "load", "manifest": "", "weights": dir, "device": "cpu"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	reply, err = recv()
	if err != nil {
		t.Fatalf("load reply: %v", err)
	}
	if reply["loaded"] != true {
		t.Fatalf("load reply: %v", reply)
	}
}
