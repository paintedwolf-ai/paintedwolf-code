import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import type { BackupRestoreResult } from "../../api/types.ts";
import { pickBackupArchive } from "../../platform/files/folder.ts";
import { confirmDestructive } from "../../platform/interaction/confirm-dialog.ts";
import { downloadExport } from "../../platform/files/save-file.ts";
import { isTauriRuntime } from "../../platform/runtime.ts";
import { hostSharesDevice } from "../../platform/connection/host-identity.ts";
import { pickNativeBackup, transferNativeBackup, type BackupTransferOptions } from "../../platform/persistence/backup-transfer.ts";
import {
  DATA_SETTINGS_COPY,
  restoreStagedMessage,
} from "./data-settings-copy.ts";

export type BackupActionOptions = BackupTransferOptions & {
  /** Overrides the handshake where the host serves none. */
  sharedDevice?: boolean;
};

/** Native transfer streams through the engine on this device. */
function usesNativeTransfer(options: BackupActionOptions | undefined): boolean {
  return isTauriRuntime() && (options?.sharedDevice ?? hostSharesDevice());
}

export type BackupActionResult =
  | { kind: "saved" }
  | { kind: "cancelled" }
  | { kind: "failed"; detail: string };

export type RestoreActionResult =
  | { kind: "staged"; message: string; result: BackupRestoreResult }
  | { kind: "cancelled" }
  | { kind: "failed"; detail: string };

export async function runBackupNow(
  client: LycaonClient,
  options?: BackupActionOptions,
): Promise<BackupActionResult> {
  try {
    if (usesNativeTransfer(options)) {
      const grant = await pickNativeBackup(false);
      if (!grant) return { kind: "cancelled" };
      const result = await transferNativeBackup(grant, false, options);
      return { kind: "cancelled" in result ? "cancelled" : "saved" };
    }
    const { blob, filename } = await client.exportBackup();
    const result = await downloadExport(blob, filename);
    return { kind: result.kind };
  } catch (err) {
    return {
      kind: "failed",
      detail: err instanceof LycaonApiError
        ? [err.message, err.suggestedAction].filter(Boolean).join(" ")
        : err instanceof Error ? err.message : typeof err === "string" ? err : DATA_SETTINGS_COPY.backupFailed,
    };
  }
}

export function confirmRestore(): Promise<boolean> {
  return confirmDestructive({
    message: DATA_SETTINGS_COPY.restoreConfirm,
    title: DATA_SETTINGS_COPY.restoreConfirmTitle,
    okLabel: DATA_SETTINGS_COPY.restoreConfirmOk,
    cancelLabel: DATA_SETTINGS_COPY.cancel,
  });
}

export async function runRestoreBackup(
  client: LycaonClient,
  options?: BackupActionOptions,
): Promise<RestoreActionResult> {
  if (usesNativeTransfer(options)) {
    try {
      const grant = await pickNativeBackup(true);
      if (!grant || !(await confirmRestore())) return { kind: "cancelled" };
      const result = await transferNativeBackup(grant, true, options);
      if ("cancelled" in result) return { kind: "cancelled" };
      if ("saved" in result) throw new Error("Invalid restore response");
      return { kind: "staged", result, message: restoreStagedMessage(result.recovery_copy_path) };
    } catch (err) {
      return { kind: "failed", detail: err instanceof Error ? err.message : String(err) };
    }
  }
  const bytes = await pickBackupArchive();
  if (!bytes) {
    return { kind: "cancelled" };
  }
  if (!(await confirmRestore())) {
    return { kind: "cancelled" };
  }
  try {
    const result = await client.restoreBackup(bytes);
    return {
      kind: "staged",
      result,
      message: restoreStagedMessage(result.recovery_copy_path),
    };
  } catch (err) {
    if (err instanceof LycaonApiError) {
      if (err.code === "backup_invalid") {
        return { kind: "failed", detail: DATA_SETTINGS_COPY.invalidBackup };
      }
      if (err.code === "backup_incompatible") {
        return { kind: "failed", detail: DATA_SETTINGS_COPY.incompatibleBackup };
      }
    }
    return {
      kind: "failed",
      detail: err instanceof Error ? err.message : DATA_SETTINGS_COPY.restoreFailed,
    };
  }
}

export async function exportSessionTranscript(
  client: LycaonClient,
  sessionId: string,
  format: "md" | "json",
): Promise<void> {
  const { blob, filename } = await client.exportSessionTranscript(sessionId, format);
  await downloadExport(blob, filename);
}
