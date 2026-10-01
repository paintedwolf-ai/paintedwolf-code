import { RecoveryUpdates } from "./update/RecoveryUpdates.tsx";
import { Show, createMemo, createSignal, onCleanup } from "solid-js";
import type { Accessor, JSX } from "solid-js";
import { LycaonApiError } from "../api/http.ts";
import {
  criticalStopRecoveryLabel,
  HOST_INCOMPATIBLE_STOP_CODE,
  resolveCriticalStop,
  STORE_INCOMPATIBLE_STOP_CODE,
  storeIncompatibleFreshRestartCriticalStop,
  storeIncompatibleSnapshotIncompatibleCriticalStop,
  storeIncompatibleRestartCriticalStop,
  type CriticalStopRecovery,
  type CriticalStopSpec,
} from "../notices/critical-stop-model.ts";
import {
  connectAppBackend,
  getLycaonClient,
  restartAppBackend,
} from "../platform/connection/app-connection.ts";
import { preflightReport } from "../platform/persistence/preflight-report.ts";
import { refreshPreflight } from "../platform/persistence/preflight-store.ts";
import {
  getBackendConnection,
  lastSidecarStartDetail,
  lastSidecarStartFailure,
  provideCredentialVaultPassword,
  resetCredentialVault,
} from "../platform/connection/backend.ts";
import { lastSeenHealth } from "../platform/connection/health.ts";
import { hostIdentity, incompatibleHost } from "../platform/connection/host-identity.ts";
import { saveReportBundle } from "../report-a-bug/report-a-bug.ts";
import { runRestoreBackup } from "../settings/storage/data-backup-actions.ts";
import { DATA_SETTINGS_COPY } from "../settings/storage/data-settings-copy.ts";
import { confirmDestructive } from "../platform/interaction/confirm-dialog.ts";
import { saveStartupDiagnosticsBundle } from "../settings/system/diagnostics-export.ts";
import type { AppStore } from "../store/app-state-model.ts";
import { CriticalStop } from "./CriticalStop.tsx";
import { stopFactsLine } from "../platform/stop-facts.ts";
import { WindowHost } from "./shell/WindowHost.tsx";
import { DenField } from "./primitives/DenField.tsx";
import { DenInput } from "./primitives/DenInput.tsx";
import { DenButton } from "./primitives/DenButton.tsx";
import { backupProgressMessage, type BackupTransferProgress } from "../platform/persistence/backup-transfer.ts";

export type CriticalStopHandle = {
  spec: Accessor<CriticalStopSpec | undefined>;
  view: () => JSX.Element;
};

type DiagnosticsSource = "startup" | "live";

const MIN_VAULT_PASSWORD_BYTES = 12;
const MAX_VAULT_PASSWORD_BYTES = 16 * 1024;

function isVaultPasswordRecovery(recovery: CriticalStopRecovery): boolean {
  return recovery === "unlock_credential_vault" ||
    recovery === "create_credential_vault";
}

export function createCriticalStop(appStore: AppStore): CriticalStopHandle {
  const facts = () => ({
    sidecarStatus: appStore.state.sidecarStatus,
    startFailure: lastSidecarStartFailure(),
    startDetail: lastSidecarStartDetail(),
    preflight: preflightReport(),
    health: lastSeenHealth(),
    host: hostIdentity(),
  });
  const offlineAvailable = createMemo<boolean>((admitted) => admitted || (
    facts().sidecarStatus === "connected" && facts().health?.status === "ok" &&
    resolveCriticalStop(facts()) === undefined
  ), false);
  const live = (): CriticalStopSpec | undefined =>
    resolveCriticalStop({ ...facts(), offlineAvailable: offlineAvailable() });

  const [attemptInFlight, setAttemptInFlight] = createSignal(false);
  const [restoreProgress, setRestoreProgress] = createSignal<BackupTransferProgress>();
  let restoreTransfer: AbortController | undefined;
  onCleanup(() => restoreTransfer?.abort());
  const [freshStartInFlight, setFreshStartInFlight] = createSignal(false);
  const [vaultResetInFlight, setVaultResetInFlight] = createSignal(false);
  const [vaultPassword, setVaultPassword] = createSignal("");
  const [vaultPasswordConfirmation, setVaultPasswordConfirmation] =
    createSignal("");
  const [restoreOutcome, setRestoreOutcome] = createSignal<
    CriticalStopSpec | undefined
  >();
  let pinned: CriticalStopSpec | undefined;

  const spec = (): CriticalStopSpec | undefined => {
    const outcome = restoreOutcome();
    if (outcome) return outcome;
    const next = live();
    if (attemptInFlight() || freshStartInFlight()) return pinned ?? next;
    pinned = next;
    return next;
  };

  const [attemptError, setAttemptError] = createSignal<string | undefined>();

  const recoveryInFlightLabel = (recovery: CriticalStopRecovery): string => {
    switch (recovery) {
      case "reconnect":
      case "recheck":
        return "Checking…";
      case "restore_snapshot":
      case "restore_backup":
        return "Restoring…";
      case "finish_restore":
        return "Finishing…";
      case "finish_fresh_start":
        return "Starting fresh…";
      case "unlock_credential_vault":
        return "Unlocking…";
      case "create_credential_vault":
        return "Creating…";
      case "reset_credential_vault":
        return "Resetting…";
    }
  };

  const clearVaultPasswordFields = (): void => {
    setVaultPassword("");
    setVaultPasswordConfirmation("");
  };

  const resetVault = async (stop: CriticalStopSpec): Promise<void> => {
    const confirmed = await confirmDestructive({
      title: "Reset credential vault?",
      message:
        "This permanently removes every saved provider key, MCP token, and managed secret from this device. Projects and conversation history are not deleted.",
      okLabel: "Reset vault",
      cancelLabel: "Cancel",
    });
    if (!confirmed) return;
    pinned = stop;
    setAttemptError(undefined);
    setVaultResetInFlight(true);
    try {
      await resetCredentialVault();
      clearVaultPasswordFields();
      await connectAppBackend(appStore).catch(() => undefined);
    } catch {
      setAttemptError(stop.retryFailed);
    } finally {
      setVaultResetInFlight(false);
    }
  };

  const attempt = (stop: CriticalStopSpec): void => {
    if (stop.recovery === "reset_credential_vault") {
      void resetVault(stop);
      return;
    }
    setAttemptError(undefined);
    setAttemptInFlight(true);
    const done = (error?: string) => {
      setAttemptError(error);
      setAttemptInFlight(false);
    };
    const restartStagedOperation = async (
      restartStop: CriticalStopSpec,
    ): Promise<void> => {
      setRestoreOutcome(restartStop);
      try {
        await restartAppBackend();
        if (lastSeenHealth()?.status !== "ok") {
          done(restartStop.retryFailed);
          return;
        }
        setRestoreOutcome(undefined);
        done();
      } catch {
        done(restartStop.retryFailed);
      }
    };
    if (isVaultPasswordRecovery(stop.recovery)) {
      const password = vaultPassword();
      const passwordBytes = new TextEncoder().encode(password).byteLength;
      if (
        passwordBytes < MIN_VAULT_PASSWORD_BYTES ||
        passwordBytes > MAX_VAULT_PASSWORD_BYTES ||
        password.trim() === ""
      ) {
        done(
          `Use an app password between ${MIN_VAULT_PASSWORD_BYTES} and ${MAX_VAULT_PASSWORD_BYTES} bytes.`,
        );
        return;
      }
      if (
        stop.recovery === "create_credential_vault" &&
        password !== vaultPasswordConfirmation()
      ) {
        done("The passwords do not match.");
        return;
      }
      provideCredentialVaultPassword(password);
      clearVaultPasswordFields();
      void connectAppBackend(appStore, { reportFailure: false }).then(
        () => done(),
        () =>
          done(
            lastSidecarStartFailure()?.startsWith("credential_vault_")
              ? undefined
              : stop.retryFailed,
          ),
      );
      return;
    }
    if (stop.recovery === "reconnect") {
      void connectAppBackend(appStore, { reportFailure: true }).then(
        () =>
          done(
            stop.code === HOST_INCOMPATIBLE_STOP_CODE && incompatibleHost()
              ? stop.retryFailed
              : undefined,
          ),
        () => done(stop.retryFailed),
      );
      return;
    }
    if (stop.recovery === "restore_snapshot") {
      const client = getLycaonClient();
      if (!client) {
        done(stop.retryFailed);
        return;
      }
      void client
        .restoreRecoverySnapshot()
        .then(() => restartStagedOperation(storeIncompatibleRestartCriticalStop()))
        .catch((err: unknown) => {
          if (err instanceof LycaonApiError && err.code === "backup_incompatible") {
            setRestoreOutcome(storeIncompatibleSnapshotIncompatibleCriticalStop());
            done();
            return;
          }
          if (err instanceof LycaonApiError && err.code === "backup_restore_pending") {
            // An existing pending marker means the snapshot is already staged.
            void restartStagedOperation(storeIncompatibleRestartCriticalStop());
            return;
          }
          done(stop.retryFailed);
        });
      return;
    }
    if (stop.recovery === "restore_backup") {
      const client = getLycaonClient();
      if (!client) {
        done(stop.retryFailed);
        return;
      }
      restoreTransfer = new AbortController();
      // The recovery engine serves no handshake; Den only reaches it on this device.
      void runRestoreBackup(client, { signal: restoreTransfer.signal, onProgress: setRestoreProgress, sharedDevice: true }).then(
        (result) => {
          setRestoreProgress(undefined);
          restoreTransfer = undefined;
          if (result.kind === "cancelled") {
            done();
            return;
          }
          if (result.kind === "failed") {
            done(result.detail);
            return;
          }
          void restartStagedOperation(storeIncompatibleRestartCriticalStop());
        },
        () => { setRestoreProgress(undefined); restoreTransfer = undefined; done(stop.retryFailed); },
      );
      return;
    }
    if (
      stop.recovery === "finish_restore" ||
      stop.recovery === "finish_fresh_start"
    ) {
      void restartAppBackend().then(
        () => {
          if (lastSeenHealth()?.status !== "ok") {
            done(stop.retryFailed);
            return;
          }
          setRestoreOutcome(undefined);
          done();
        },
        () => done(stop.retryFailed),
      );
      return;
    }
    void refreshPreflight().then((outcome) =>
      done(outcome === "refreshed" ? undefined : stop.retryFailed)
    );
  };

  const startFresh = async (stop: CriticalStopSpec): Promise<void> => {
    const confirmed = await confirmDestructive({
      title: DATA_SETTINGS_COPY.startFreshConfirmTitle,
      message: DATA_SETTINGS_COPY.startFreshConfirm,
      okLabel: DATA_SETTINGS_COPY.startFreshConfirmOk,
      cancelLabel: DATA_SETTINGS_COPY.cancel,
    });
    if (!confirmed) return;
    const client = getLycaonClient();
    if (!client) {
      setAttemptError(DATA_SETTINGS_COPY.startFreshFailed);
      return;
    }
    pinned = stop;
    setAttemptError(undefined);
    setFreshStartInFlight(true);
    try {
      try {
        await client.resetStore();
      } catch (err) {
        // An existing pending marker means the fresh start is already staged.
        if (
          !(
            err instanceof LycaonApiError &&
            err.code === "backup_restore_pending"
          )
        ) {
          throw err;
        }
      }
      const restartStop = storeIncompatibleFreshRestartCriticalStop();
      setRestoreOutcome(restartStop);
      try {
        await restartAppBackend();
        if (lastSeenHealth()?.status !== "ok") {
          setAttemptError(restartStop.retryFailed);
          return;
        }
        setRestoreOutcome(undefined);
      } catch {
        setAttemptError(restartStop.retryFailed);
      }
    } catch {
      setAttemptError(DATA_SETTINGS_COPY.startFreshFailed);
    } finally {
      setFreshStartInFlight(false);
    }
  };

  const [saving, setSaving] = createSignal(false);
  const [saveStatus, setSaveStatus] = createSignal<string | undefined>();

  const saveBundle = async (source: DiagnosticsSource): Promise<void> => {
    setSaving(true);
    setSaveStatus(undefined);
    try {
      const step = source === "startup"
        ? await saveStartupDiagnosticsBundle()
        : await saveReportBundle(getBackendConnection());
      if (step.kind === "saved") {
        setSaveStatus(`Saved to ${step.path}.`);
        return;
      }
      setSaveStatus(
        step.kind === "explain"
          ? step.error
          : step.kind === "failed"
            ? step.detail
            : undefined,
      );
    } finally {
      setSaving(false);
    }
  };

  const vaultPasswordForm = (stop: CriticalStopSpec): JSX.Element | undefined => {
    if (!isVaultPasswordRecovery(stop.recovery)) return undefined;
    const creating = stop.recovery === "create_credential_vault";
    return (
      <form
        class="den-critical-stop__vault-form"
        data-testid="credential-vault-password-form"
        onSubmit={(event) => {
          event.preventDefault();
          if (!attemptInFlight() && !vaultResetInFlight()) attempt(stop);
        }}
      >
        <DenField
          label="App password"
          hint={creating
            ? "Use at least 12 bytes. This password cannot be recovered."
            : undefined}
        >
          <DenInput
            type="password"
            autocomplete={creating ? "new-password" : "current-password"}
            data-testid="credential-vault-password"
            value={vaultPassword()}
            disabled={attemptInFlight() || vaultResetInFlight()}
            onInput={(event) => setVaultPassword(event.currentTarget.value)}
          />
        </DenField>
        <Show when={creating}>
          <DenField label="Confirm app password">
            <DenInput
              type="password"
              autocomplete="new-password"
              data-testid="credential-vault-password-confirmation"
              value={vaultPasswordConfirmation()}
              disabled={attemptInFlight() || vaultResetInFlight()}
              onInput={(event) =>
                setVaultPasswordConfirmation(event.currentTarget.value)}
            />
          </DenField>
        </Show>
      </form>
    );
  };

  const view = (): JSX.Element => (
    <WindowHost testId="critical-stop-host">
      <Show when={spec()} keyed>
        {(stop) => (
          <CriticalStop
            code={stop.code}
            title={stop.title}
            message={
              attemptError()
                ? `${stop.message} ${attemptError()}`
                : stop.message
            }
            detail={stop.detail}
            children={<>{vaultPasswordForm(stop)}
              <Show when={restoreProgress()} keyed>{(progress) => <div>
                <p role="status">{backupProgressMessage(progress)}</p>
                <Show when={progress.cancellable}><DenButton variant="secondary" onClick={() => restoreTransfer?.abort()}>Cancel transfer</DenButton></Show>
              </div>}</Show>
              <RecoveryUpdates /></>}
            primaryAction={{
              label: attemptInFlight() || vaultResetInFlight()
                ? recoveryInFlightLabel(stop.recovery)
                : criticalStopRecoveryLabel(stop.recovery),
              disabled:
                attemptInFlight() || freshStartInFlight() || vaultResetInFlight(),
              onClick: () => attempt(stop),
            }}
            secondaryAction={
              isVaultPasswordRecovery(stop.recovery)
                ? {
                    label: vaultResetInFlight()
                      ? "Resetting…"
                      : "Forgot password? Reset vault…",
                    disabled: attemptInFlight() || vaultResetInFlight(),
                    onClick: () => resetVault(stop),
                  }
                : (stop.recovery === "restore_snapshot" ||
                stop.recovery === "restore_backup")
                ? {
                    label: freshStartInFlight()
                      ? DATA_SETTINGS_COPY.startingFresh
                      : DATA_SETTINGS_COPY.startFresh,
                    disabled: attemptInFlight() || freshStartInFlight(),
                    onClick: () => startFresh(stop),
                  }
                : (stop.recovery === "reconnect" ||
                      (stop.recovery === "recheck" &&
                        stop.code !== STORE_INCOMPATIBLE_STOP_CODE))
                  ? {
                      label: saving() ? "Saving…" : "Save report bundle…",
                      disabled: saving(),
                      onClick: () =>
                        saveBundle(
                          stop.recovery === "reconnect" ? "startup" : "live",
                        ),
                    }
                  : undefined
            }
            facts={stopFactsLine(stop.catastrophicDetail, lastSeenHealth())}
            diagnostic={stop.diagnostic}
            status={saveStatus()}
          />
        )}
      </Show>
    </WindowHost>
  );

  return { spec, view };
}
