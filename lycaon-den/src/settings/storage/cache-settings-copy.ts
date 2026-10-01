import type { LocalDataBucketId } from "../../api/types.ts";

/** Advanced → Cache copy; it never names the engine. */
export const CACHE_SETTINGS_COPY = {
  intro:
    "Clear rebuildable caches and scratch on this device. Chats, API keys, projects, and project overlays are not removed.",
  loadError: "Could not load cache status.",
  clearError: "Could not clear cache.",
  clearing: "Clearing…",
  cleared: "Cleared",
  clearAction: "Clear",
  clearAll: "Clear all",
  clearAllLabel: "Clear all rebuildable data",
  clearAllHint: "Removes every cache and scratch bucket listed above.",
  clearAllConfirmTitle: "Clear all rebuildable data",
  clearBucketTitle: "Clear cache",
  cancel: "Cancel",
  empty: "Empty",
  present: "Present",
  connectHint: "Connect to the backend to manage cache.",
  unknownBucketLabel: "Cache",
  durableKeepNote: "Chats, keys, and projects are kept.",
} as const;

type BucketCopy = { label: string; hint: string; clearLabel: string };

const KNOWN_BUCKET_COPY: Record<LocalDataBucketId, BucketCopy> = {
  web_index: {
    label: "Web research index",
    hint: "Cached pages and warming history for web research.",
    clearLabel: "Clear web research index",
  },
  source_observations: {
    label: "Project file observations",
    hint: "Rebuildable file timestamps and digests used to accelerate project refresh.",
    clearLabel: "Clear project file observations",
  },
  source_catalog: {
    label: "Project file index",
    hint: "Rebuildable file tree, search, and file picker data. Open views keep the data they need until closed. Repository files are kept.",
    clearLabel: "Clear project file index",
  },
  fetch_cache: {
    label: "Fetched pages",
    hint: "Bodies saved from opening web pages.",
    clearLabel: "Clear fetched pages",
  },
  osv_cache: {
    label: "Vulnerability database",
    hint: "Shared scanner database download.",
    clearLabel: "Clear vulnerability database",
  },
  modelfeed: {
    label: "Model catalog",
    hint: "Last-good models.dev catalog snapshot.",
    clearLabel: "Clear model catalog",
  },
  pricing_cache: {
    label: "Pricing rates",
    hint: "Downloaded pricing tables used for spend estimates.",
    clearLabel: "Clear pricing rates",
  },
  browser_cache: {
    label: "Managed browser",
    hint: "Browser binary and ephemeral browse profiles.",
    clearLabel: "Clear managed browser",
  },
  extension_cache: {
    label: "Extension packages",
    hint: "Downloaded extension and suite package bodies. Desired installs and locks are kept.",
    clearLabel: "Clear extension packages",
  },
  debug_logs: {
    label: "Debug logs",
    hint: "Diagnostic JSONL captures. Does not turn off full debug logging.",
    clearLabel: "Clear debug logs",
  },
  scan_scratch: {
    label: "Scanner scratch",
    hint: "Temporary scan output and cached verify-command detection.",
    clearLabel: "Clear scanner scratch",
  },
  worker_branches: {
    label: "Worker branches",
    hint: "Working copies kept for completed worker changes awaiting review. Cleared copies are rebuilt when needed; running work is kept.",
    clearLabel: "Clear worker branches",
  },
  session_scratch: {
    label: "Session scratch",
    hint: "Working files agents keep for each chat, removed when the chat is deleted. Clearing keeps running chats' files; cleared files can't be restored.",
    clearLabel: "Clear session scratch",
  },
};

/** Label/hint for a host bucket id. Unknown ids get a generic label (no path). */
export function cacheBucketCopy(id: string): BucketCopy {
  const known = KNOWN_BUCKET_COPY[id as LocalDataBucketId];
  if (known) return known;
  return {
    label: CACHE_SETTINGS_COPY.unknownBucketLabel,
    hint: id,
    clearLabel: `Clear ${id}`,
  };
}

export function cacheClearBucketConfirm(id: string): string {
  const { label } = cacheBucketCopy(id);
  return `Clear “${label}”? ${CACHE_SETTINGS_COPY.durableKeepNote} It can be rebuilt later.`;
}

export function cacheClearAllConfirm(labels: string[]): string {
  const list =
    labels.length === 0
      ? "all rebuildable buckets"
      : labels.map((l) => `• ${l}`).join("\n");
  return `Clear all rebuildable cache and scratch?\n\n${list}\n\n${CACHE_SETTINGS_COPY.durableKeepNote}`;
}

/** Best-effort human size for status rows. */
export function formatCacheBytes(n: number | undefined): string {
  if (n == null || !Number.isFinite(n) || n <= 0) return "";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10 * 1024 ? 1 : 0)} KB`;
  if (n < 1024 * 1024 * 1024) {
    return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  }
  if (n < 1024 * 1024 * 1024 * 1024) {
    return `${(n / (1024 * 1024 * 1024)).toFixed(1)} GB`;
  }
  return `${(n / (1024 * 1024 * 1024 * 1024)).toFixed(1)} TB`;
}

/** Status badge text: size when present, otherwise Empty. */
export function cachePresenceLabel(
  present: boolean,
  bytes: number | undefined,
): string {
  if (!present) return CACHE_SETTINGS_COPY.empty;
  const size = formatCacheBytes(bytes);
  return size || CACHE_SETTINGS_COPY.present;
}
