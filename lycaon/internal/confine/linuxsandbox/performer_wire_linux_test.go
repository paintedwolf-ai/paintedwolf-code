//go:build linux

package linuxsandbox

import "testing"

func TestPathCallFramesRoundTrip(t *testing.T) {
	want := pathCall{Op: callRename, Absolute: 2, Dirs: 2, Mode: 0o644, Umask: 0o22, Flags: 1 << 40,
		Dev: 7, Resolve: 3, Notification: 1<<63 + 5, Caller: 4242, Path: "a", NewPath: "b/c", Content: ""}
	raw, err := encodePathCall(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodePathCall(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("decoded %+v, want %+v", got, want)
	}
}

func TestPathCallFramesRefuseMalformedInput(t *testing.T) {
	if _, err := encodePathCall(pathCall{Op: "chmod"}); err == nil {
		t.Fatal("encoded an unknown op")
	}
	if _, err := encodePathCall(pathCall{Op: callOpen, Dirs: 3}); err == nil {
		t.Fatal("encoded three directories")
	}
	raw, err := encodePathCall(pathCall{Op: callSymlink, Dirs: 1, Path: "name", Content: "target"})
	if err != nil {
		t.Fatal(err)
	}
	bad := map[string][]byte{
		"short":     raw[:pathCallHeader-1],
		"truncated": raw[:len(raw)-1],
		"trailing":  append(append([]byte{}, raw...), 0),
	}
	for _, mutate := range []struct {
		name string
		at   int
		b    byte
	}{{"op zero", 0, 0}, {"op unknown", 0, byte(len(callOps))}, {"dirs", 2, 3}, {"reserved", 3, 1}, {"caller", 51, 0xff}} {
		b := append([]byte{}, raw...)
		b[mutate.at] = mutate.b
		bad[mutate.name] = b
	}
	for name, b := range bad {
		if _, err := decodePathCall(b); err == nil {
			t.Errorf("%s: decoded a malformed frame", name)
		}
	}
}
