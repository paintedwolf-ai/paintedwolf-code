package editoroutbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

type seeded struct {
	directory string
	header    string
	log       string
}

func frameFor(record string) (frame, []byte) {
	raw := []byte(record + "\n")
	sum := sha256.Sum256(raw)
	var identity struct {
		Kind        string `json:"kind"`
		ClientID    string `json:"clientId"`
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(raw, &identity); err != nil {
		panic(err)
	}
	if identity.Kind == "checkpoint" {
		identity.OperationID = "checkpoint"
	}
	name := sha256.Sum256([]byte(identity.ClientID + "\x00" + identity.Kind + "\x00" + identity.OperationID))
	return frame{Name: hex.EncodeToString(name[:]), Kind: identity.Kind, Length: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}, raw
}

// seedEnvelope writes one committed transaction: an unsynchronized checkpoint and a pending update.
func seedEnvelope(t *testing.T, root string) seeded {
	t.Helper()
	unsynchronized := false
	checkpoint, checkpointRaw := frameFor(`{"kind":"checkpoint","documentId":"document","clientId":"window","state":"AAA=","synchronized":false}`)
	checkpoint.Pending, checkpoint.Synchronized = []string{"pending"}, &unsynchronized
	update, updateRaw := frameFor(`{"kind":"update","documentId":"document","clientId":"window","operationId":"pending","update":"AAA=","acknowledged":false}`)
	update.Offset = checkpoint.Length
	log := slices.Concat(checkpointRaw, updateRaw)
	h := header{Format: format.Version, Address: address{DocumentID: "document", ProjectID: "project", RootID: "root", FileID: "file", Path: "file.txt"},
		Log: format.LogPrefix + "1" + format.LogSuffix, LogBytes: int64(len(log)), Frames: []frame{checkpoint, update}}
	raw, err := json.Marshal(h)
	testutil.FailErr(t, "encode header", err)
	id := sha256.Sum256([]byte("document"))
	directory := filepath.Join(root, format.Directory, hex.EncodeToString(id[:]))
	testutil.FailErr(t, "create document directory", os.MkdirAll(directory, 0o700))
	testutil.FailErr(t, "write records", os.WriteFile(filepath.Join(directory, h.Log), log, 0o600))
	testutil.FailErr(t, "write header", os.WriteFile(filepath.Join(directory, format.Header), raw, 0o600))
	return seeded{directory: directory, header: filepath.Join(directory, format.Header), log: filepath.Join(directory, h.Log)}
}

func TestValidateAcceptsAnUnpublishedTailAndCompactedGenerations(t *testing.T) {
	root := t.TempDir()
	envelope := seedEnvelope(t, root)
	testutil.FailErr(t, "validate seeded envelope", Validate(t.Context(), root))
	log, err := os.OpenFile(envelope.log, os.O_APPEND|os.O_WRONLY, 0o600)
	testutil.FailErr(t, "open records", err)
	_, err = log.WriteString(`{"kind":"format","documentId":"document","clientId":"win`)
	testutil.FailErr(t, "append an interrupted transaction", err)
	testutil.FailErr(t, "close records", log.Close())
	testutil.FailErr(t, "validate with an unpublished tail", Validate(t.Context(), root))
	testutil.FailErr(t, "leave a retired generation temporary", os.WriteFile(filepath.Join(envelope.directory, format.LogPrefix+"2"+format.LogSuffix+".tmp"), []byte("partial"), 0o600))
	testutil.FailErr(t, "validate beside a compaction temporary", Validate(t.Context(), root))
}

func TestValidateRefusesCorruptionAndFutureFormatsWithoutChangingWork(t *testing.T) {
	for _, change := range []string{"format", "body", "identity", "acknowledgement", "frame", "short"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			envelope := seedEnvelope(t, root)
			testutil.FailErr(t, "validate native envelope", Validate(t.Context(), root))
			rawHeader, err := os.ReadFile(envelope.header)
			testutil.FailErr(t, "read header", err)
			rawLog, err := os.ReadFile(envelope.log)
			testutil.FailErr(t, "read records", err)
			var h header
			testutil.FailErr(t, "decode header", json.Unmarshal(rawHeader, &h))
			modifiedLog := append([]byte(nil), rawLog...)
			switch change {
			case "format":
				h.Format++
			case "body":
				modifiedLog[len(modifiedLog)-2] ^= 1
			case "identity":
				h.Address.DocumentID = "other"
			case "acknowledgement":
				h.Synchronized = true
			case "frame":
				h.Frames[1].Offset++
			case "short":
				modifiedLog = modifiedLog[:len(modifiedLog)-1]
			}
			modifiedHeader, err := json.Marshal(h)
			testutil.FailErr(t, "encode modified header", err)
			testutil.FailErr(t, "seed incompatible header", os.WriteFile(envelope.header, modifiedHeader, 0o600))
			testutil.FailErr(t, "seed incompatible records", os.WriteFile(envelope.log, modifiedLog, 0o600))
			if err := Validate(t.Context(), root); err == nil {
				t.Fatal("incompatible envelope accepted")
			}
			afterHeader, err := os.ReadFile(envelope.header)
			testutil.FailErr(t, "read original header", err)
			afterLog, err := os.ReadFile(envelope.log)
			testutil.FailErr(t, "read original records", err)
			if string(afterHeader) != string(modifiedHeader) || string(afterLog) != string(modifiedLog) {
				t.Fatal("validation modified pending work")
			}
		})
	}
}

func TestCaptureLockRefusesCanceledContext(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	release, err := Acquire(ctx, root)
	if release != nil {
		release()
		t.Fatal("canceled request acquired the capture lock")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquisition returned %v, want context.Canceled", err)
	}
	ctx, cancel = context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	release, err = Acquire(ctx, root)
	testutil.FailErr(t, "acquire after canceled request", err)
	release()
}

func TestCaptureLockSurvivesDirectoryReplacementAndCancelsWait(t *testing.T) {
	root := t.TempDir()
	seedEnvelope(t, root)
	release, err := Acquire(t.Context(), root)
	testutil.FailErr(t, "hold native commit lock", err)
	defer release()
	testutil.FailErr(t, "replace outbox tree", os.RemoveAll(filepath.Join(root, format.Directory)))
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err = Acquire(ctx, root)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("capture acquired through replacement: %v", err)
	}
	release()
	release, err = Acquire(t.Context(), root)
	testutil.FailErr(t, "acquire after prior writer exits", err)
	release()
}
