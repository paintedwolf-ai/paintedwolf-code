import type { HistoryRetentionPolicy, HistoryRetentionRule, HistoryStorageLane } from "../../api/types.ts";

export const HISTORY_CLASSES = [
  { id: "recordings", label: "Recordings", loss: "Replay of removed recordings" },
  { id: "checkpoints", label: "Rewind checkpoints", loss: "Rewind to removed checkpoints" },
  { id: "source_revisions", label: "File revision bodies", loss: "Opening removed historical file versions" },
  { id: "scan_detail", label: "Scan details", loss: "Detailed results from removed scans" },
  { id: "receipt_detail", label: "Model usage receipts", loss: "Individual model call details" },
] as const;

export type HistoryClass = (typeof HISTORY_CLASSES)[number]["id"];
export const RETENTION_MODES = [
  { value: "forever", label: "Keep forever" },
  { value: "max_age", label: "Keep for a number of days" },
  { value: "max_bytes", label: "Keep within a storage limit" },
] as const;

export function historyBytes(value: number): string {
  if (value < 1024) return `${value} B`;
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KiB`;
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MiB`;
  return `${(value / 1024 ** 3).toFixed(1)} GiB`;
}

export function retentionRule(mode: HistoryRetentionRule["mode"]): HistoryRetentionRule {
  if (mode === "max_age") return { mode, max_age_days: 30 };
  if (mode === "max_bytes") return { mode, max_bytes: 1024 ** 3 };
  return { mode: "forever" };
}

export function retentionValid(policy: HistoryRetentionPolicy): boolean {
  return HISTORY_CLASSES.every(({ id }) => {
    const rule = policy[id];
    if (rule.mode === "forever") return true;
    const value = rule.mode === "max_age" ? rule.max_age_days : rule.max_bytes;
    return value != null && Number.isSafeInteger(value) && value > 0;
  });
}

export function retentionLosses(policy: HistoryRetentionPolicy): string {
  return HISTORY_CLASSES.filter(({ id }) => policy[id].mode !== "forever")
    .map(({ loss }) => loss).join("; ");
}

const STORAGE_LANES: Readonly<Record<string, string>> = {
  "store.db": "History database", "store.db-wal": "Recent database writes",
  "store.db-shm": "Database coordination", "store.db-journal": "Database rollback journal",
  checkpoint_content: "Checkpoint content", source_content: "File revision content",
  model_and_evidence: "Model responses and evidence", recordings: "Recordings",
  artifacts: "Saved artifacts", artifact_cleanup: "Artifact cleanup pending", "upgrade-recovery": "Upgrade recovery snapshots",
  "document-outbox": "Preserved editor work", debug_logs: "Diagnostic logs", "worker-branches": "Worker branches",
  "worker-baselines": "Worker baselines", attachments: "Attachments", drafts: "Draft workspaces",
  web_index: "Web search index", source_observations: "Source observations", source_catalog: "Source catalog",
  fetch_cache: "Downloaded page cache", osv_cache: "Vulnerability cache", modelfeed: "Model catalog cache",
  pricing_cache: "Pricing cache", browser_cache: "Browser cache", extension_cache: "Extension cache", scan_scratch: "Temporary scan files",
  "session-checkpoints": "Chat checkpoint storage", session_scratch: "Session scratch files",
};

export function historyLaneLabel(id: string): string {
  return STORAGE_LANES[id] ?? "Other retained data";
}

export function historyLaneUsage(lane: HistoryStorageLane): string {
  if (lane.id === "upgrade-recovery") {
    return `${historyBytes(lane.logical_bytes)} retained snapshot content · may share disk space with live files and other snapshots`;
  }
  const logical = lane.logical_bytes > 0 ? ` · ${historyBytes(lane.logical_bytes)} uncompressed` : "";
  const shared = lane.shared_bytes > 0 ? ` · ${historyBytes(lane.shared_bytes)} shared` : "";
  return `${historyBytes(lane.stored_bytes)} stored${logical}${shared}`;
}
