import { createSignal } from "solid-js";
import {
  clearThumbnails,
  dropThumbnail,
  parseThumbnails,
  putThumbnail,
  shouldRecaptureThumbnail,
  type ThumbnailMap,
} from "./thumbnail-cache.ts";
import {
  activeThemePaintKey,
  onActiveThemeChange,
} from "../settings/appearance/appearance-prefs.ts";

const STORAGE_KEY = "den.project-thumbnails.v1";

type PersistedThumbnailState = {
  paintKey: string | null;
  thumbnails: ThumbnailMap;
};

function load(): PersistedThumbnailState {
  if (typeof localStorage === "undefined") {
    return { paintKey: null, thumbnails: {} };
  }
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "");
    if (!parsed || typeof parsed !== "object") {
      return { paintKey: null, thumbnails: {} };
    }
    const record = parsed as Record<string, unknown>;
    const paintKey = typeof record.paintKey === "string" ? record.paintKey : null;
    return {
      paintKey,
      thumbnails: parseThumbnails(JSON.stringify(record.thumbnails ?? {})),
    };
  } catch {
    return { paintKey: null, thumbnails: {} };
  }
}

function persist(map: ThumbnailMap, paintKey: string | null): void {
  if (typeof localStorage === "undefined") return;
  try {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ paintKey, thumbnails: map } satisfies PersistedThumbnailState),
    );
  } catch {
    /* Storage exhaustion skips persistence. */
  }
}

const initial = load();
const [thumbnails, setThumbnails] = createSignal<ThumbnailMap>(
  initial.thumbnails,
);
let thumbnailPaintKey = initial.paintKey;

export function projectThumbnail(projectId: string): string | null {
  const currentPaint = activeThemePaintKey();
  if (!currentPaint || thumbnailPaintKey !== currentPaint) return null;
  return thumbnails()[projectId]?.dataUrl ?? null;
}

export function shouldCaptureProjectThumbnail(
  projectId: string,
  now: number = Date.now(),
): boolean {
  const currentPaint = activeThemePaintKey();
  if (!currentPaint) return false;
  if (thumbnailPaintKey !== currentPaint) return true;
  return shouldRecaptureThumbnail(thumbnails(), projectId, now);
}

export function recordProjectThumbnail(
  projectId: string,
  dataUrl: string,
  paintKey: string | null = activeThemePaintKey(),
): void {
  if (!paintKey || paintKey !== activeThemePaintKey()) return;
  reconcileThumbnailPaint(paintKey);
  const next = putThumbnail(thumbnails(), projectId, dataUrl, Date.now());
  setThumbnails(next);
  persist(next, thumbnailPaintKey);
}

function reconcileThumbnailPaint(paintKey: string): void {
  if (paintKey === thumbnailPaintKey) return;
  thumbnailPaintKey = paintKey;
  forgetAllProjectThumbnails();
}

export function forgetProjectThumbnail(projectId: string): void {
  const next = dropThumbnail(thumbnails(), projectId);
  if (next === thumbnails()) return;
  setThumbnails(next);
  persist(next, thumbnailPaintKey);
}

/** Clears captured client previews without touching product covers. */
export function forgetAllProjectThumbnails(): void {
  const next = clearThumbnails(thumbnails());
  if (next !== thumbnails()) setThumbnails(next);
  persist(next, thumbnailPaintKey);
}

/** Persisted snapshots match the palette that painted them. */
export function setupProjectThumbnailThemeInvalidation(): () => void {
  const currentPaint = activeThemePaintKey();
  if (currentPaint) reconcileThumbnailPaint(currentPaint);
  return onActiveThemeChange(reconcileThumbnailPaint);
}
