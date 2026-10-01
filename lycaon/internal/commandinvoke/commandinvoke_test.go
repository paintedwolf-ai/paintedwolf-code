package commandinvoke

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"net/http"
	"testing"
)

func TestRequestDigestIsStableAndInputSensitive(t *testing.T) {
	req := api.CommandInvokeRequest{OperationID: "op-1", FrameRevision: "frame-1"}
	first, err := RequestDigest("project", "p1", "acme/a:x", req)
	testutil.FailErr(t, "first digest", err)
	repeated, err := RequestDigest("project", "p1", "acme/a:x", req)
	testutil.FailErr(t, "repeated digest", err)
	if first != repeated {
		t.Fatal("identical input must digest identically")
	}
	sessionDigest, err := RequestDigest("session", "p1", "acme/a:x", req)
	testutil.FailErr(t, "session digest", err)
	if first == sessionDigest {
		t.Fatal("route scope must participate in the digest")
	}
	changed := req
	changed.Args = map[string]any{"q": "x"}
	changedDigest, err := RequestDigest("project", "p1", "acme/a:x", changed)
	testutil.FailErr(t, "changed digest", err)
	if first == changedDigest {
		t.Fatal("request body must participate in the digest")
	}
}

func TestResponseEncodeDecodeRoundTrip(t *testing.T) {
	encoded, err := EncodeResponse(http.StatusAccepted, api.CommandInvokeResponse{
		Status: "accepted", FrameRevision: "frame-1", MessageID: "11111111-1111-4111-8111-111111111111",
	})
	testutil.FailErr(t, "encode", err)
	status, body, err := DecodeResponse(encoded)
	testutil.FailErr(t, "decode", err)
	if status != http.StatusAccepted || body.MessageID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("round trip = %d %+v", status, body)
	}
}

func TestSQLReceiptsLoadSave(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "receipts.db")

	store := SQLReceipts{DB: sqlDB}
	ctx := context.Background()
	if _, found, err := store.Load(ctx, "op-1"); err != nil || found {
		t.Fatalf("empty load = found=%v err=%v", found, err)
	}
	receipt := Receipt{OperationID: "op-1", InputDigest: "digest", ResponseJSON: `{"status":200}`}
	testutil.FailErr(t, "save", store.Save(ctx, receipt))
	loaded, found, err := store.Load(ctx, "op-1")
	testutil.FailErr(t, "load", err)
	if !found || loaded != receipt {
		t.Fatalf("loaded = %+v found=%v", loaded, found)
	}
	if err := store.Save(ctx, Receipt{OperationID: "op-1", InputDigest: "other", ResponseJSON: `{}`}); err == nil {
		t.Fatal("duplicate receipt replaced the stored outcome")
	}
	loaded, found, err = store.Load(ctx, "op-1")
	testutil.FailErr(t, "reload", err)
	if !found || loaded != receipt {
		t.Fatalf("reloaded = %+v found=%v", loaded, found)
	}
}
