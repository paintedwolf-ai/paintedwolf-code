export const DATA_SETTINGS_COPY = {
  heading: "Backup & restore",
  secretsNote:
    "Backups contain your conversation and project data. Device credentials, MCP connections, and extension setup are excluded. Restoring on this device keeps its existing credentials; a new device needs credentials and integrations configured again.",
  backupNow: "Back up now",
  backingUp: "Preparing backup…",
  backupSaved: "Backup saved.",
  restore: "Restore…",
  restoring: "Staging restore…",
  restoreConfirmTitle: "Restore from backup?",
  restoreConfirm:
    "Restore from this backup? This replaces your current data. A recovery copy of your current data is saved first, and the app must restart.",
  restoreConfirmOk: "Restore",
  cancel: "Cancel",
  offline: "Backup and restore require a connected backend.",
  backupFailed: "Could not create the backup.",
  restoreFailed: "Could not stage the restore.",
  restoreRestartFailed:
    "Restore is staged, but the backend could not restart. Restart the app before doing any more work.",
  restoreRestarted: "Restore applied. Reconnecting to your restored data…",
  invalidBackup: "This file isn't a valid backup.",
  incompatibleBackup: "This app cannot upgrade the backup's data format. Install a compatible version and try again.",
  startFresh: "Start fresh…",
  startingFresh: "Starting fresh…",
  startFreshConfirmTitle: "Start fresh?",
  startFreshConfirm:
    "Start fresh with a clean data store? Conversations, projects, and other data tied to this store will be removed from the app. A recovery copy is saved first. Provider settings and credentials stay on this device.",
  startFreshConfirmOk: "Start fresh",
  startFreshFailed:
    "A fresh start could not be prepared. Your current data was not changed.",
} as const;

export function restoreStagedMessage(recoveryPath: string): string {
  return `Restore staged. Restart Painted Wolf Code to finish. Your previous data was saved to ${recoveryPath}.`;
}
