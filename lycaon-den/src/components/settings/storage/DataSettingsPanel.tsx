import { Show, createSignal, onCleanup } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import {
  runBackupNow,
  runRestoreBackup,
} from "../../../settings/storage/data-backup-actions.ts";
import { DATA_SETTINGS_COPY } from "../../../settings/storage/data-settings-copy.ts";
import { isTauriRuntime } from "../../../platform/runtime.ts";
import { restartAppBackend } from "../../../platform/connection/app-connection.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { backupProgressMessage, type BackupTransferProgress } from "../../../platform/persistence/backup-transfer.ts";
import { HistoryStorageSettingsPanel } from "./HistoryStorageSettingsPanel.tsx";
import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";

type Props = {
  client: LycaonClient | null | undefined;
};

export function DataSettingsPanel(props: Props) {
  const [busy, setBusy] = createSignal<"backup" | "restore" | null>(null);
  const [statusMessage, setStatusMessage] = createSignal<string | undefined>();
  const [actionError, setActionError] = createSignal<string | undefined>();
  const [stagedMessage, setStagedMessage] = createSignal<string | undefined>();
  const [progress, setProgress] = createSignal<BackupTransferProgress>();
  let transfer: AbortController | undefined;
  onCleanup(() => transfer?.abort());
  const beginTransfer = () => {
    transfer = new AbortController();
    setProgress(undefined);
    return { signal: transfer.signal, onProgress: setProgress };
  };

  const onBackup = async () => {
    const c = props.client;
    if (!c) return;
    setBusy("backup");
    setActionError(undefined);
    setStatusMessage(undefined);
    try {
      const result = await runBackupNow(c, beginTransfer());
      if (result.kind === "failed") {
        setActionError(result.detail);
      } else if (result.kind === "saved") {
        setStatusMessage(DATA_SETTINGS_COPY.backupSaved);
      }
    } finally {
      setBusy(null);
      setProgress(undefined);
      transfer = undefined;
    }
  };

  const onRestore = async () => {
    const c = props.client;
    if (!c) return;
    setBusy("restore");
    setActionError(undefined);
    setStatusMessage(undefined);
    try {
      const result = await runRestoreBackup(c, beginTransfer());
      if (result.kind === "failed") {
        setActionError(result.detail);
        setStagedMessage(undefined);
      } else if (result.kind === "staged") {
        setStagedMessage(result.message);
        setStatusMessage(undefined);
        if (!isTauriRuntime()) return;
        try {
          await restartAppBackend();
          setStagedMessage(DATA_SETTINGS_COPY.restoreRestarted);
        } catch (err) {
          setActionError(
            err instanceof Error ? err.message : DATA_SETTINGS_COPY.restoreRestartFailed,
          );
        }
      }
    } finally {
      setBusy(null);
      setProgress(undefined);
      transfer = undefined;
    }
  };

  return (
    <section class="den-settings-section" data-testid="data-settings-panel">
      <Show
        when={props.client}
        fallback={
          <p class="den-settings-hint" data-testid="data-settings-offline">
            {DATA_SETTINGS_COPY.offline}
          </p>
        }
      >
        <div class="den-settings-subsection" {...settingAnchor("backup-restore")}>
          <h3 class="den-settings-subhead">{settingLabel("backup-restore")}</h3>

          <p class="den-settings-hint" data-testid="data-backup-secrets-note">
            {DATA_SETTINGS_COPY.secretsNote}
          </p>

          <div class="den-settings-actions">
            <DenButton
              variant="secondary"
              data-testid="data-backup-now"
              disabled={busy() != null}
              onClick={() => void onBackup()}
            >
              {busy() === "backup"
                ? DATA_SETTINGS_COPY.backingUp
                : DATA_SETTINGS_COPY.backupNow}
            </DenButton>
            <DenButton
              variant="secondary"
              data-testid="data-restore"
              disabled={busy() != null}
              onClick={() => void onRestore()}
            >
              {busy() === "restore"
                ? DATA_SETTINGS_COPY.restoring
                : DATA_SETTINGS_COPY.restore}
            </DenButton>
          </div>
        </div>

        <Show when={progress()} keyed>
          {(value) => <div class="den-settings-actions">
            <p class="den-settings-hint" role="status">{backupProgressMessage(value)}</p>
            <Show when={value.cancellable}>
              <DenButton variant="secondary" onClick={() => transfer?.abort()}>Cancel transfer</DenButton>
            </Show>
          </div>}
        </Show>

        <Show when={stagedMessage()} keyed>
          {(text) => (
            <p
              class="den-settings-hint"
              role="status"
              data-testid="data-restore-staged"
            >
              {text}
            </p>
          )}
        </Show>
        <Show when={statusMessage()} keyed>
          {(text) => (
            <p class="den-settings-hint" data-testid="data-backup-status" role="status">
              {text}
            </p>
          )}
        </Show>
        <Show when={actionError()} keyed>
          {(text) => (
            <p class="den-settings-warn" data-testid="data-action-error" role="alert">
              {text}
            </p>
          )}
        </Show>
        <HistoryStorageSettingsPanel client={props.client} />
      </Show>
    </section>
  );
}
