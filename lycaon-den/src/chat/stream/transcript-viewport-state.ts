import type {
  DenTranscriptViewportSnapshot,
  DenTranscriptViewportState,
} from "../../../shared/app-state-types.ts";
import { isTauriRuntime } from "../../platform/runtime.ts";
import { getAppStateSnapshot } from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";
import {
  parseTranscriptDisclosureKey,
  type TranscriptDisclosureKey,
} from "../transcript/presentation/transcript-disclosure-key.ts";

const TRANSCRIPT_VIEWPORT_WINDOW_CAP = 8;
const TRANSCRIPT_VIEWPORT_SESSION_CAP = 24;
const TRANSCRIPT_VIEWPORT_DISCLOSURE_CAP = 128;

let windowLabel = "main";

export async function resolveTranscriptViewportWindowLabel(): Promise<void> {
  if (!isTauriRuntime()) {
    windowLabel = "main";
    return;
  }
  try {
    const { getCurrentWindow } = await import("@tauri-apps/api/window");
    windowLabel = getCurrentWindow().label.trim() || "main";
  } catch {
    windowLabel = "main";
  }
}

export function setTranscriptViewportWindowLabelForTests(label: string): void {
  windowLabel = label.trim() || "main";
}

function cleanOpenKeys(value: unknown): TranscriptDisclosureKey[] {
  if (!Array.isArray(value)) return [];
  const keys = new Set<TranscriptDisclosureKey>();
  for (const candidate of value) {
    if (typeof candidate !== "string") continue;
    const key = parseTranscriptDisclosureKey(candidate);
    if (!key) continue;
    keys.add(key);
    if (keys.size >= TRANSCRIPT_VIEWPORT_DISCLOSURE_CAP) break;
  }
  return [...keys];
}

function parsePosition(
  value: unknown,
): DenTranscriptViewportSnapshot["position"] {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return undefined;
  }
  const row = value as Record<string, unknown>;
  const rowKey = typeof row.rowKey === "string" ? row.rowKey.trim() : "";
  if (!rowKey) return undefined;
  const rowOffsetPx =
    typeof row.rowOffsetPx === "number" && Number.isFinite(row.rowOffsetPx)
      ? row.rowOffsetPx
      : 0;
  return { rowKey, rowOffsetPx };
}

function parseSnapshot(value: unknown): DenTranscriptViewportSnapshot | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return null;
  }
  const row = value as Record<string, unknown>;
  const position = parsePosition(row.position);
  return {
    ...(position ? { position } : {}),
    openKeys: cleanOpenKeys(row.openKeys),
    touchedAt:
      typeof row.touchedAt === "number" && Number.isFinite(row.touchedAt)
        ? row.touchedAt
        : 0,
  };
}

export function parseTranscriptViewportState(
  value: unknown,
): DenTranscriptViewportState | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return undefined;
  }
  const rawWindows = (value as { byWindow?: unknown }).byWindow;
  if (
    typeof rawWindows !== "object" ||
    rawWindows === null ||
    Array.isArray(rawWindows)
  ) {
    return undefined;
  }
  const windows: DenTranscriptViewportState["byWindow"] = {};
  const windowEntries = Object.entries(rawWindows as Record<string, unknown>)
    .flatMap(([label, value]) => {
      if (!label.trim() || typeof value !== "object" || value === null) return [];
      const row = value as Record<string, unknown>;
      const rawSessions = row.bySession;
      if (
        typeof rawSessions !== "object" ||
        rawSessions === null ||
        Array.isArray(rawSessions)
      ) {
        return [];
      }
      const bySession = Object.fromEntries(
        Object.entries(rawSessions as Record<string, unknown>)
          .flatMap(([sessionId, snapshot]) => {
            const id = sessionId.trim();
            const parsed = parseSnapshot(snapshot);
            return id && parsed ? [[id, parsed] as const] : [];
          })
          .sort((a, b) => b[1].touchedAt - a[1].touchedAt)
          .slice(0, TRANSCRIPT_VIEWPORT_SESSION_CAP),
      );
      if (Object.keys(bySession).length === 0) return [];
      const touchedAt =
        typeof row.touchedAt === "number" && Number.isFinite(row.touchedAt)
          ? row.touchedAt
          : Math.max(...Object.values(bySession).map((entry) => entry.touchedAt));
      return [[label.trim(), { touchedAt, bySession }] as const];
    })
    .sort((a, b) => b[1].touchedAt - a[1].touchedAt)
    .slice(0, TRANSCRIPT_VIEWPORT_WINDOW_CAP);
  for (const [label, row] of windowEntries) windows[label] = row;
  return Object.keys(windows).length > 0 ? { byWindow: windows } : undefined;
}

/** Saved disclosures that still name a transcript surface. */
export function transcriptViewportOpenKeys(
  snapshot: DenTranscriptViewportSnapshot | undefined,
): TranscriptDisclosureKey[] {
  return cleanOpenKeys(snapshot?.openKeys);
}

export function transcriptViewportSnapshot(
  sessionId: string,
): DenTranscriptViewportSnapshot | undefined {
  const id = sessionId.trim();
  if (!id) return undefined;
  return getAppStateSnapshot().transcriptViewport?.byWindow[windowLabel]
    ?.bySession[id];
}

function newest<T extends { touchedAt: number }>(
  record: Record<string, T>,
  cap: number,
): Record<string, T> {
  return Object.fromEntries(
    Object.entries(record)
      .sort((a, b) => b[1].touchedAt - a[1].touchedAt)
      .slice(0, cap),
  );
}

function sameSnapshot(
  current: DenTranscriptViewportSnapshot | undefined,
  next: Omit<DenTranscriptViewportSnapshot, "touchedAt">,
): boolean {
  if (!current) return false;
  if ((current.position?.rowKey ?? "") !== (next.position?.rowKey ?? "")) {
    return false;
  }
  const currentOffset = current.position?.rowOffsetPx ?? 0;
  if (Math.abs(currentOffset - (next.position?.rowOffsetPx ?? 0)) > 0.5) {
    return false;
  }
  const currentOpen = cleanOpenKeys(current.openKeys);
  const nextOpen = cleanOpenKeys(next.openKeys);
  return (
    currentOpen.length === nextOpen.length &&
    currentOpen.every((key, index) => key === nextOpen[index])
  );
}

/** A deleted chat keeps no saved position in any window. */
export function forgetTranscriptViewport(sessionId: string): void {
  const id = sessionId.trim();
  const current = getAppStateSnapshot().transcriptViewport;
  if (!id || !current) return;
  let changed = false;
  const byWindow: DenTranscriptViewportState["byWindow"] = {};
  for (const [label, row] of Object.entries(current.byWindow)) {
    if (!(id in row.bySession)) {
      byWindow[label] = row;
      continue;
    }
    changed = true;
    const bySession = Object.fromEntries(
      Object.entries(row.bySession).filter(([session]) => session !== id),
    );
    if (Object.keys(bySession).length > 0) byWindow[label] = { ...row, bySession };
  }
  if (!changed) return;
  void persistAppStateInBackground({
    transcriptViewport: Object.keys(byWindow).length > 0 ? { byWindow } : undefined,
  });
}

export function persistTranscriptViewportSnapshot(
  sessionId: string,
  snapshot: Omit<DenTranscriptViewportSnapshot, "touchedAt">,
): void {
  const id = sessionId.trim();
  if (!id) return;
  const current = getAppStateSnapshot().transcriptViewport;
  const cleanedSnapshot = {
    ...snapshot,
    openKeys: cleanOpenKeys(snapshot.openKeys),
  };
  if (
    sameSnapshot(current?.byWindow[windowLabel]?.bySession[id], cleanedSnapshot)
  ) {
    return;
  }
  const touchedAt = Date.now();
  const bySession = newest(
    {
      ...(current?.byWindow[windowLabel]?.bySession ?? {}),
      [id]: {
        ...cleanedSnapshot,
        touchedAt,
      },
    },
    TRANSCRIPT_VIEWPORT_SESSION_CAP,
  );
  const byWindow = newest(
    {
      ...(current?.byWindow ?? {}),
      [windowLabel]: { touchedAt, bySession },
    },
    TRANSCRIPT_VIEWPORT_WINDOW_CAP,
  );
  void persistAppStateInBackground({ transcriptViewport: { byWindow } });
}
