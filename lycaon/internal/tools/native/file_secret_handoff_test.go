package native

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestFileSecretHandoffFollowsSave(t *testing.T) {
	for _, mode := range []string{"refused", "stale", "saved"} {
		t.Run(mode, func(t *testing.T) {
			database := testdbfixture.Open(t, "store.db")
			testdbseed.InsertSession(t, database, "root-1", testdbseed.DefaultProjectID)
			values := credentialstore.NewEmpty(credentialstore.Slot{Path: filepath.Join(t.TempDir(), credentialstore.VaultBasename), Namespace: credentialstore.NamespaceManagedSecrets, Context: "file secret test"}, func(id string) bool { _, err := uuid.Parse(id); return err == nil })
			service := secretcap.NewWithStore(database, values, nil)
			owner := testdbseed.OwnerID(t, database)
			secret, err := service.CreateSettingsSecret(t.Context(), secretcap.CreateSettingsSecretRequest{ProjectID: testdbseed.DefaultProjectID, PersonID: owner, OperationID: "create", Name: "File credential", Purpose: "save", Value: "person-held-file-value"})
			testutil.FailErr(t, "create held secret", err)
			fp, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("f", 32)))
			testutil.FailErr(t, "create fingerprinter", err)
			service.SetFingerprinter(fp)
			unlocks := presence.NewUnlocks(presence.NewBroker())
			service.SetUnlocks(unlocks)
			opened := time.Now().UTC()
			now := opened
			unlocks.SetClock(func() time.Time { return now })
			const unlockID = "11111111-1111-4111-8111-111111111111"
			unlocks.Open(presence.NewUnlock("root-1", presence.Verified{ChallengeID: unlockID, PersonID: owner, Authenticator: presence.AuthenticatorMacOS, VerifiedAt: opened}))
			resolution, err := service.Resolve(t.Context(), map[string]any{"content": secret.Reference}, secretcap.ResolveContext{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1", ToolName: "write", ToolCallID: "save"})
			testutil.FailErr(t, "resolve file secret", err)
			resolution.ApproveRelease(secretcap.Release{Fingerprints: []secretmatch.SecretFingerprint{fp.Fingerprint("person-held-file-value")}, Recipients: []secretmatch.Recipient{{ID: "file:.env", Label: "Local file: .env", Surface: secretmatch.SurfaceFile, Kind: secretmatch.DestinationFile}}})
			testutil.FailErr(t, "precheck file", resolution.Precheck(t.Context(), nil))
			now = opened.Add(time.Minute)
			root := t.TempDir()
			tc := nativefixture.Context(root)
			tc.Effects.Secrets = resolution
			tc.Invocation.Contract.SecretReferenceSurface = toolcontract.SecretSurfaceFile
			tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error {
				if mode == "refused" {
					return errors.New("review refused")
				}
				return nil
			}
			base := ""
			if mode == "stale" {
				base = strings.Repeat("a", 64)
			}
			err = applyAgentFile(t.Context(), tc, testMutationTarget(filepath.Join(root, ".env")), []byte("DB_PASSWORD=person-held-file-value"), nil, base)
			if (err == nil) != (mode == "saved") {
				t.Fatalf("save error = %v", err)
			}
			resolution.Finish(t.Context())
			history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, secret.Reference, 0)
			testutil.FailErr(t, "read use history", err)
			use := history.Items[0]
			unlock, _ := unlocks.Active("root-1")
			if mode == "saved" {
				if use.Delivery != secretcap.DeliveryHandedOff || use.UnlockID != unlockID || !unlock.LastUsedAt.Equal(now) {
					t.Fatalf("saved use=%+v unlock=%+v", use, unlock)
				}
			} else if use.Delivery == secretcap.DeliveryHandedOff || use.UnlockID != "" || !unlock.LastUsedAt.Equal(opened) {
				t.Fatalf("failed use=%+v unlock=%+v", use, unlock)
			}
		})
	}
}
