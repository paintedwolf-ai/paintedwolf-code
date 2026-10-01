package extpacks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func prepareMetaTransactionFixture(t *testing.T, previous bool) (*metaRootTransaction, string, string, string) {
	t.Helper()
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	source := t.TempDir()
	testutil.FailErr(t, "write source meta", os.WriteFile(filepath.Join(source, MetaPackFileName), []byte(
		"manifest_version: 1\nid: acme/kit\nname: Kit\nversion: 1.0.0\ncompatibility:\n  extension_api: ^1.0.0\nmembers:\n  - acme/member\nconflicts_with: []\nextends: []\n",
	), 0o600))
	target, err := CachedMetaPackDir("acme/kit")
	testutil.FailErr(t, "target", err)
	if previous {
		testutil.FailErr(t, "mkdir previous target", os.MkdirAll(target, 0o700))
		testutil.FailErr(t, "write previous meta", os.WriteFile(filepath.Join(target, MetaPackFileName), []byte("previous\n"), 0o600))
	}
	transaction, err := prepareMetaRootTransaction(source, target, PackKindPath, MetaPackMetadata{
		MetaPackID: "acme/kit", Version: "1.0.0", Kind: PackKindPath,
	})
	testutil.FailErr(t, "prepare transaction", err)
	desiredPath := filepath.Join(t.TempDir(), "extensions.yaml")
	lockPath := filepath.Join(filepath.Dir(desiredPath), "extensions.lock.yaml")
	testutil.FailErr(t, "bind transaction", transaction.bind(desiredPath, lockPath, []byte("next desired\n"), []byte("next lock\n")))
	return transaction, target, desiredPath, lockPath
}

func TestRecoverMetaPackTransactionRollsBackWithoutSelectingState(t *testing.T) {
	transaction, target, _, _ := prepareMetaTransactionFixture(t, true)
	testutil.FailErr(t, "publish transaction", transaction.publish())
	testutil.FailErr(t, "recover transactions", RecoverMetaPackTransactions())

	data, err := os.ReadFile(filepath.Join(target, MetaPackFileName))
	testutil.FailErr(t, "read restored meta", err)
	if string(data) != "previous\n" {
		t.Fatalf("restored meta = %q", data)
	}
	if _, err := os.Stat(transaction.Directory); !os.IsNotExist(err) {
		t.Fatalf("transaction directory survives rollback: %v", err)
	}
}

func TestRecoverMetaPackTransactionFinalizesSelectingState(t *testing.T) {
	transaction, target, desiredPath, lockPath := prepareMetaTransactionFixture(t, true)
	testutil.FailErr(t, "publish transaction", transaction.publish())
	testutil.FailErr(t, "write desired", os.WriteFile(desiredPath, []byte("next desired\n"), 0o600))
	testutil.FailErr(t, "write lock", os.WriteFile(lockPath, []byte("next lock\n"), 0o600))
	testutil.FailErr(t, "recover transactions", RecoverMetaPackTransactions())

	data, err := os.ReadFile(filepath.Join(target, MetaPackFileName))
	testutil.FailErr(t, "read committed meta", err)
	if string(data) == "previous\n" {
		t.Fatal("published meta was rolled back despite matching selecting state")
	}
	if _, err := os.Stat(transaction.Directory); !os.IsNotExist(err) {
		t.Fatalf("transaction directory survives finalize: %v", err)
	}
}

func TestRecoverMetaPackTransactionRemovesNewRootWithoutSelectingState(t *testing.T) {
	transaction, target, _, _ := prepareMetaTransactionFixture(t, false)
	testutil.FailErr(t, "publish transaction", transaction.publish())
	testutil.FailErr(t, "recover transactions", RecoverMetaPackTransactions())
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("unselected new meta root survives rollback: %v", err)
	}
}

func TestRecoverMetaPackTransactionDoesNotAdoptUnpublishedMatchingState(t *testing.T) {
	transaction, target, desiredPath, lockPath := prepareMetaTransactionFixture(t, true)
	testutil.FailErr(t, "write matching desired", os.WriteFile(desiredPath, []byte("next desired\n"), 0o600))
	testutil.FailErr(t, "write matching lock", os.WriteFile(lockPath, []byte("next lock\n"), 0o600))
	testutil.FailErr(t, "recover transactions", RecoverMetaPackTransactions())

	data, err := os.ReadFile(filepath.Join(target, MetaPackFileName))
	testutil.FailErr(t, "read unchanged meta", err)
	if string(data) != "previous\n" {
		t.Fatalf("unpublished transaction changed meta root: %q", data)
	}
	if _, err := os.Stat(transaction.Directory); !os.IsNotExist(err) {
		t.Fatalf("unpublished transaction directory survives recovery: %v", err)
	}
}

func TestRecoverMetaPackTransactionsFinalizesRemovalTombstone(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	root, err := MetaPackCacheRoot()
	testutil.FailErr(t, "meta cache root", err)
	tombstone := filepath.Join(root, metaRemovalPrefix+"stale")
	testutil.FailErr(t, "mkdir removal tombstone", os.MkdirAll(tombstone, 0o700))
	testutil.FailErr(t, "write removed body", os.WriteFile(filepath.Join(tombstone, "removed"), []byte("stale"), 0o600))

	testutil.FailErr(t, "recover transactions", RecoverMetaPackTransactions())
	if _, err := os.Stat(tombstone); !os.IsNotExist(err) {
		t.Fatalf("removal tombstone survives recovery: %v", err)
	}
}
