import type { BackupRestoreResult } from "../../api/types.ts";
import { nativePathDialog } from "../files/native-path-dialog.ts";
import { listenHostEvent } from "../windows/window-channel.ts";

export type BackupTransferProgress = {
  id: string;
  phase: "preparing" | "uploading" | "validating" | "downloading" | "saving";
  bytes: number;
  total: number | null;
  cancellable: boolean;
};

export type BackupTransferOptions = {
  signal?: AbortSignal;
  onProgress?: (progress: BackupTransferProgress) => void;
};

export async function pickNativeBackup(restore: boolean): Promise<string | null> {
  const picked = await nativePathDialog(restore ? "Restore from backup" : "Save backup", restore
    ? { kind: "file", filter: { name: "Backup", extensions: ["zip"] } }
    : { kind: "save", fileName: `painted-wolf-backup-${new Date().toISOString().slice(0, 10)}.zip` });
  return picked?.grant ?? null;
}

export async function transferNativeBackup(
  grant: string,
  restore: boolean,
  options: BackupTransferOptions = {},
): Promise<BackupRestoreResult | { cancelled: true } | { saved: true }> {
  if (options.signal?.aborted) return { cancelled: true };
  const { invoke } = await import("@tauri-apps/api/core");
  const id = crypto.randomUUID();
  const cancel = () => { void invoke("cancel_backup_transfer", { id }).catch(() => undefined); };
  const unlisten = await listenHostEvent<BackupTransferProgress>("backup-transfer-progress", ({ payload }) => {
    if (payload.id !== id) return;
    options.onProgress?.(payload);
    // The first native progress event also confirms operation registration.
    if (options.signal?.aborted && payload.cancellable) cancel();
  });
  options.signal?.addEventListener("abort", cancel, { once: true });
  try {
    if (options.signal?.aborted) return { cancelled: true };
    const result = invoke<BackupRestoreResult | { cancelled: true } | { saved: true }>("transfer_backup", { id, grant, restore });
    if (options.signal?.aborted) cancel();
    return await result;
  } finally {
    options.signal?.removeEventListener("abort", cancel);
    unlisten();
  }
}

export function backupProgressMessage(progress: BackupTransferProgress): string {
  if (progress.phase === "preparing") return "Preparing backup…";
  if (progress.phase === "validating") return "Checking and staging restore…";
  if (progress.phase === "saving") return "Saving backup…";
  const amount = (progress.bytes / (1024 * 1024)).toLocaleString(undefined, { maximumFractionDigits: 1 });
  const total = progress.total == null ? "" : ` of ${(progress.total / (1024 * 1024)).toLocaleString(undefined, { maximumFractionDigits: 1 })}`;
  return `${progress.phase === "uploading" ? "Uploading" : "Saving"} ${amount}${total} MiB…`;
}
