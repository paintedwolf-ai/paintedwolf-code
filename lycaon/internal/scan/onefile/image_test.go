package onefile

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func fixtureLauncher() []byte {
	raw := make([]byte, 64)
	copy(raw, []byte{'\x7f', 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(raw[16:], 3)
	binary.LittleEndian.PutUint16(raw[18:], 62)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint16(raw[52:], 64)
	return raw
}

func encodedImage(t *testing.T, image *Image) []byte {
	t.Helper()
	var output bytes.Buffer
	if err := Write(&output, image); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestOnefilePreservesOrderedFilesChecksumsAndLinks(t *testing.T) {
	image := &Image{Launcher: fixtureLauncher(), Members: []Member{
		{Name: "opengrep.bin", Executable: true, Data: []byte("native program")},
		{Name: "lib/data", Data: []byte{0, 1, 2}},
		{Name: "lib/alias", Link: "data"},
	}}
	raw := encodedImage(t, image)
	restored, err := Read(raw)
	if err != nil || !reflect.DeepEqual(restored, image) {
		t.Fatalf("restored payload=%+v error=%v", restored, err)
	}
	restored.Members[0].Data = []byte("relocated native program")
	changed := encodedImage(t, restored)
	reloaded, err := Read(changed)
	if err != nil || !reflect.DeepEqual(reloaded, restored) || bytes.Equal(raw, changed) {
		t.Fatalf("rewritten payload=%+v error=%v", reloaded, err)
	}
	if dataChecksum(nil) != 1 {
		t.Fatal("zero CRC was not mapped to the launcher's sentinel")
	}
}

func rawPayload(t *testing.T, plain []byte) []byte {
	t.Helper()
	var packed bytes.Buffer
	packed.Write(fixtureLauncher())
	packed.WriteString("KAY")
	writer, err := zstd.NewWriter(&packed, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	size := packed.Len() - 64
	if err := binary.Write(&packed, binary.LittleEndian, uint64(size)); err != nil {
		t.Fatal(err)
	}
	return packed.Bytes()
}

func TestOnefileRefusesAmbiguousOrCorruptPayloads(t *testing.T) {
	valid := encodedImage(t, &Image{Launcher: fixtureLauncher(), Members: []Member{{Name: "program", Data: []byte("body")}}})
	for name, raw := range map[string][]byte{
		"short":               valid[:8],
		"truncated trailer":   valid[:len(valid)-1],
		"unknown compression": bytes.Replace(valid, []byte("KAY"), []byte("KAX"), 1),
		"invalid ELF":         append([]byte("bad!"), valid[4:]...),
		"unterminated name":   rawPayload(t, []byte("missing terminator")),
		"path escape":         rawPayload(t, []byte("../outside\x00")),
		"unknown flags":       rawPayload(t, []byte("file\x00\xff")),
		"link escape":         rawPayload(t, []byte("file\x00\x02../outside\x00\x00")),
		"empty members":       rawPayload(t, []byte{0}),
		"long name":           rawPayload(t, append(bytes.Repeat([]byte{'x'}, 1024), 0)),
		"wrong checksum":      rawPayload(t, []byte("file\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x02\x00\x00\x00x\x00")),
		"expanded size":       rawPayload(t, []byte("file\x00\x00\xff\xff\xff\xff\xff\xff\xff\xff\x01\x00\x00\x00")),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Read(raw); err == nil {
				t.Fatal("invalid attached payload was accepted")
			}
		})
	}
}

func TestOnefileWriterRefusesUnsafeMemberSets(t *testing.T) {
	for _, members := range [][]Member{nil, {{Name: "../outside"}}, {{Name: "same"}, {Name: "same"}}, {{Name: "link", Link: "/outside"}}, {{Name: "link", Link: "target", Data: []byte("ambiguous")}}} {
		var output bytes.Buffer
		if err := Write(&output, &Image{Launcher: fixtureLauncher(), Members: members}); err == nil {
			t.Fatal("unsafe output payload accepted")
		}
	}
}
