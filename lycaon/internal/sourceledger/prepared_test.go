package sourceledger

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type observedWriter struct {
	db.Handle
	beforeBegin func()
}

func (d observedWriter) BeginTx(ctx context.Context, options *sql.TxOptions) (*sql.Tx, error) {
	d.beforeBegin()
	return d.Handle.BeginTx(ctx, options)
}

func TestHistoryPublishesObjectsBeforeAcquiringWriter(t *testing.T) {
	for _, kind := range []string{"record", "batch", "track", "refresh"} {
		t.Run(kind, func(t *testing.T) {
			store, ctx := openLedger(t)
			if kind == "refresh" {
				_, err := store.TrackFile(ctx, TrackInput{ProjectID: "p1", RootID: "r1", Path: "a", Content: []byte("original")})
				testutil.FailErr(t, "baseline", err)
			}
			content := []byte("prepared outside the writer")
			begun := 0
			store.sqlDB = observedWriter{Handle: store.sqlDB, beforeBegin: func() {
				begun++
				got, err := store.objects.GetSHA(sourceblob.ContentSHA(content))
				testutil.FailErr(t, "object must exist before writer admission", err)
				if !bytes.Equal(got, content) {
					t.Fatal("published content mismatch")
				}
			}}
			input := RecordInput{ProjectID: "p1", RootID: "r1", Path: "a", Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginExternal, After: content}
			var err error
			switch kind {
			case "record":
				err = store.Record(ctx, input)
			case "batch":
				err = store.RecordBatch(ctx, []RecordInput{input})
			default:
				_, err = store.TrackFile(ctx, TrackInput{ProjectID: "p1", RootID: "r1", Path: "a", Content: content})
			}
			testutil.FailErr(t, "record", err)
			if begun == 0 {
				t.Fatal("no writer transaction observed")
			}
		})
	}
}

func TestPreparedContentDoesNotNeedTheDatabaseWriter(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(map[bool]string{false: "recording", true: "held edit"}[held], func(t *testing.T) {
			store, ctx := openLedger(t)
			tracked, err := store.TrackFile(ctx, TrackInput{ProjectID: "p1", RootID: "r1", Path: "a", Content: []byte("base")})
			testutil.FailErr(t, "track", err)
			tx, err := store.sqlDB.BeginTx(ctx, nil)
			testutil.FailErr(t, "hold writer", err)
			defer tx.Rollback()
			done := make(chan error, 1)
			var recording PreparedRecording
			var edit PreparedHeldEdit
			go func() {
				if held {
					edit, err = store.PrepareHeldEdit(ctx, HeldEdit{ProjectID: "p1", RootID: "r1", Path: "a", FileID: tracked.FileID, Content: []byte("held")})
				} else {
					recording, err = store.Prepare(ctx, []RecordInput{{ProjectID: "p1", RootID: "r1", Path: "a", Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal, After: []byte("saved")}})
				}
				done <- err
			}()
			select {
			case err := <-done:
				testutil.FailErr(t, "prepare while writer occupied", err)
			case <-time.After(5 * time.Second):
				_ = tx.Rollback()
				<-done
				if recording != nil {
					recording.Close()
				}
				if edit != nil {
					edit.Close()
				}
				t.Fatal("content preparation waited for the database writer")
			}
			// Transaction application must not return to the object publisher.
			objects := store.objects
			blocked := filepath.Join(t.TempDir(), "not-a-directory")
			testutil.FailErr(t, "block publication", os.WriteFile(blocked, []byte("blocked"), 0600))
			store.objects = sourceblob.New(blocked)
			defer func() { store.objects = objects }()
			if held {
				defer edit.Close()
				_, err = edit.CommitTx(ctx, tx)
			} else {
				defer recording.Close()
				_, err = recording.CommitTx(ctx, tx)
			}
			testutil.FailErr(t, "join caller transaction", err)
			testutil.FailErr(t, "commit", tx.Commit())
		})
	}
}

func TestPreparationFreezesInputsAndReleasesRetention(t *testing.T) {
	store, ctx := openLedger(t)
	original := []byte("original")
	state := &TextState{DocumentID: "doc", Epoch: 1, Spans: []TextSpan{{Length: 8, Client: 1}}}
	inputs := []RecordInput{{ProjectID: "p1", RootID: "r1", Path: "a", Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginExternal, After: original, TextAfter: state}}
	recording, err := store.Prepare(ctx, inputs)
	testutil.FailErr(t, "prepare", err)
	defer recording.Close()
	original[0] = 'x'
	inputs[0].Path = "changed"
	state.Spans[0].Client = 99
	frozen := recording.(*preparedRecording).inputs[0]
	if string(frozen.After) != "original" || frozen.Path != "a" || frozen.TextAfter.Spans[0].Client != 1 {
		t.Fatal("preparation retained caller-owned inputs")
	}
	if release, ok, err := store.objects.TryAcquireMaintenanceLease(); err != nil || ok {
		if ok {
			release()
		}
		t.Fatalf("prepared object was unprotected: %v", err)
	}
	recording.Close()
	recording.Close()
	if _, err := recording.CommitTx(ctx, nil); err == nil {
		t.Fatal("closed recording was accepted")
	}
	release, ok, err := store.objects.TryAcquireMaintenanceLease()
	testutil.FailErr(t, "maintenance lease", err)
	if !ok {
		t.Fatal("close leaked retention lease")
	}
	release()
}

func TestPreparationFailureDoesNotLeakRetention(t *testing.T) {
	store, ctx := openLedger(t)
	for _, cancelled := range []bool{false, true} {
		request, cancel := context.WithCancel(ctx)
		if cancelled {
			cancel()
		}
		_, err := store.Prepare(request, []RecordInput{{ProjectID: "p1", RootID: "r1", Path: "a", Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginExternal, After: []byte("content"), AfterSHA256: sourceblob.ContentSHA([]byte("different"))}})
		cancel()
		if err == nil || (cancelled && !errors.Is(err, context.Canceled)) {
			t.Fatalf("preparation error: %v", err)
		}
		release, ok, err := store.objects.TryAcquireMaintenanceLease()
		testutil.FailErr(t, "maintenance lease", err)
		if !ok {
			t.Fatal("failed preparation leaked retention lease")
		}
		release()
	}
}
