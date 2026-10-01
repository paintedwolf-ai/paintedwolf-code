package inboundwrite

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/textfile"
)

type previewCapture struct {
	content []byte
	size    int64
}

func (c *previewCapture) Write(p []byte) (int, error) {
	c.size += int64(len(p))
	if c.size <= sourceledger.MaxRevisionContentBytes {
		c.content = append(c.content, p...)
	} else {
		c.content = nil
	}
	return len(p), nil
}

func (c *previewCapture) text() (string, string) {
	if c.size > sourceledger.MaxRevisionContentBytes {
		return "", "This file is too large for a text preview."
	}
	if c.size == 0 {
		return "", ""
	}
	value, _, err := textfile.Decode(c.content, textfile.LimitsForRaw(sourceledger.MaxRevisionContentBytes))
	if err != nil {
		return "", "This file has no text preview."
	}
	return value, ""
}

func readTargetPreview(target fseffect.Target) (previewCapture, string, error) {
	var captured previewCapture
	f, err := target.Open()
	if os.IsNotExist(err) {
		return captured, "", nil
	}
	if err != nil {
		return captured, "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(&captured, h), f); err != nil {
		return captured, "", err
	}
	return captured, hex.EncodeToString(h.Sum(nil)), nil
}

func verifyTargetPreview(target fseffect.Target, expected string) error {
	_, hash, err := readTargetPreview(target)
	if err != nil {
		return err
	}
	if hash != expected {
		return fmt.Errorf("file changed while reviewing the replacement")
	}
	return nil
}
