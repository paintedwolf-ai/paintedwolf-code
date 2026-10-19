package onefile

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

type countedWriter struct {
	writer io.Writer
	bytes  int64
}

func (writer *countedWriter) Write(raw []byte) (int, error) {
	if writer.bytes+int64(len(raw)) > MaxBytes {
		return 0, fmt.Errorf("onefile output exceeds its bound")
	}
	n, err := writer.writer.Write(raw)
	writer.bytes += int64(n)
	return n, err
}

// Write restores a compressed payload and its terminal length after ELF fixups.
func Write(destination io.Writer, image *Image) error {
	if image == nil || len(image.Members) == 0 || len(image.Members) > maxMembers {
		return fmt.Errorf("onefile output requires a bounded member set")
	}
	if err := validateLauncher(image.Launcher); err != nil {
		return err
	}
	if _, err := destination.Write(image.Launcher); err != nil {
		return err
	}
	payload := &countedWriter{writer: destination}
	if _, err := payload.Write([]byte("KAY")); err != nil {
		return err
	}
	encoder, err := zstd.NewWriter(payload, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithWindowSize(8<<20))
	if err != nil {
		return err
	}
	defer func() { _ = encoder.Close() }()
	plain := &countedWriter{writer: encoder}
	seen := make(map[string]bool)
	for _, member := range image.Members {
		if !validName(member.Name) || len(member.Name) > 1023 || seen[member.Name] {
			return fmt.Errorf("onefile output name is invalid or repeated")
		}
		seen[member.Name] = true
		if _, err := io.WriteString(plain, member.Name+"\x00"); err != nil {
			return err
		}
		flags := byte(0)
		if member.Executable {
			flags = 1
		}
		if member.Link != "" {
			if !validLink(member.Name, member.Link) || len(member.Link) > 1023 || len(member.Data) != 0 || member.Executable {
				return fmt.Errorf("onefile output link is invalid")
			}
			flags = 2
		}
		if _, err := plain.Write([]byte{flags}); err != nil {
			return err
		}
		if flags == 2 {
			if _, err := io.WriteString(plain, member.Link+"\x00"); err != nil {
				return err
			}
			continue
		}
		if err := binary.Write(plain, binary.LittleEndian, uint64(len(member.Data))); err != nil {
			return err
		}
		if err := binary.Write(plain, binary.LittleEndian, dataChecksum(member.Data)); err != nil {
			return err
		}
		if _, err := io.Copy(plain, bytes.NewReader(member.Data)); err != nil {
			return err
		}
	}
	if _, err := plain.Write([]byte{0}); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	return binary.Write(destination, binary.LittleEndian, uint64(payload.bytes))
}
