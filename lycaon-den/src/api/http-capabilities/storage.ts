import { formatQuery, lycaonDownload, type JsonRequester } from "../http.ts";
import type { BackendConnection } from "../../platform/connection/backend.ts";
import type {
  HistoryStorageStatus,
  HistoryRetentionPolicy,
  HistoryRetentionRequest,
  HistoryRetentionPreview,
  HistoryPruneResult,
  HistoryProtection,
  LocalDataStatus,
  LocalDataClearRequest,
  LocalDataClearResponse,
  BackupRestoreResult,
  BackupCapabilities,
} from "../types.ts";

export interface StorageClient {
  getHistoryStorage(projectId?: string): Promise<HistoryStorageStatus>;
  updateHistoryStorage(req: HistoryRetentionRequest, projectId?: string): Promise<HistoryRetentionPolicy>;
  previewHistoryRetention(req: HistoryRetentionRequest): Promise<HistoryRetentionPreview>;
  pruneHistory(req: HistoryRetentionRequest): Promise<HistoryPruneResult>;
  createHistoryProtection(req: HistoryProtection): Promise<HistoryProtection>;
  deleteHistoryProtection(protectionId: string): Promise<void>;
  getLocalData(): Promise<LocalDataStatus>;
  clearLocalData(req: LocalDataClearRequest): Promise<LocalDataClearResponse>;
  /** Downloads an installation backup zip excluding secret stores. */
  exportBackup(): Promise<{ blob: Blob; filename: string }>;
  getBackupCapabilities(): Promise<BackupCapabilities>;
  /** Stage a backup archive for apply on next launch. */
  restoreBackup(archive: Uint8Array | ArrayBuffer | Blob): Promise<BackupRestoreResult>;
  /** Stage the rolling recovery snapshot for apply on next launch. */
  restoreRecoverySnapshot(): Promise<BackupRestoreResult>;
  /** Preserve current data and stage a clean current store for next launch. */
  resetStore(): Promise<BackupRestoreResult>;
}

export function createStorageClient(j: JsonRequester, connection: BackendConnection): StorageClient {
  return {
    getHistoryStorage: (projectId) =>
      j(`/v1/history-storage${formatQuery({ project_id: projectId })}`),
    updateHistoryStorage: (req, projectId) =>
      j(`/v1/history-storage${formatQuery({ project_id: projectId })}`, {
        method: "PATCH",
        body: JSON.stringify(req),
      }),
    previewHistoryRetention: (req) =>
      j("/v1/history-storage/preview", {
        method: "POST",
        body: JSON.stringify(req),
      }),
    pruneHistory: (req) =>
      j("/v1/history-storage/prune", {
        method: "POST",
        body: JSON.stringify(req),
      }),
    createHistoryProtection: (req) =>
      j("/v1/history-storage/protections", {
        method: "POST",
        body: JSON.stringify(req),
      }),
    deleteHistoryProtection: (protectionId) =>
      j(`/v1/history-storage/protections/${encodeURIComponent(protectionId)}`, {
        method: "DELETE",
      }),
    getLocalData: () => j("/v1/local-data"),
    clearLocalData: (req) =>
      j("/v1/local-data/clear", {
        method: "POST",
        body: JSON.stringify(req),
      }),
    getBackupCapabilities: () => j("/v1/backup/capabilities"),
    exportBackup: async () => {
      const stamp = new Date().toISOString().slice(0, 10);
      const download = await lycaonDownload(
        connection,
        "/v1/backup",
        `painted-wolf-backup-${stamp}.zip`,
      );
      return { blob: download.blob, filename: download.filename };
    },
    restoreBackup: async (archive) => {
      let body: Blob;
      if (archive instanceof Blob) {
        body = archive;
      } else if (archive instanceof ArrayBuffer) {
        body = new Blob([archive], { type: "application/zip" });
      } else {
        const copy = new Uint8Array(archive.byteLength);
        copy.set(archive);
        body = new Blob([copy], { type: "application/zip" });
      }
      const limits = await j<import("../types.ts").BackupCapabilities>("/v1/backup/capabilities");
      if (body.size === 0 || body.size > limits.max_archive_bytes) {
        throw new Error(`Choose a non-empty backup no larger than ${Math.floor(limits.max_archive_bytes / 1024 ** 3)} GiB.`);
      }
      return j("/v1/backup/restore", {
        method: "POST",
        body,
        headers: { "Content-Type": "application/zip" },
      });
    },
    restoreRecoverySnapshot: () =>
      j("/v1/backup/restore/recovery-snapshot", { method: "POST" }),
    resetStore: () => j("/v1/store/reset", { method: "POST" }),
  };
}
